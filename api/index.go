package api

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const (
	githubRepoURL    = "https://github.com/tbxark/vercel-proxy"
	identityEncoding = "identity"

	corsAllowOrigin  = "*"
	corsAllowMethods = "POST, GET, OPTIONS, PUT, DELETE"
	corsAllowHeaders = "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, X-PROXY-HOST, X-PROXY-SCHEME"
)

var (
	proxyURLPattern = regexp.MustCompile(`^/*(https?:)/*`)
	defaultProxy    = mustNewProxy(Config{})
)

// Config controls proxy behavior without reading environment variables.
type Config struct {
	// Socks5Proxy routes all outbound upstream requests through a SOCKS5 proxy.
	// It accepts either "host:port" or a "socks5://host:port" / "socks5h://host:port" URL.
	Socks5Proxy string `json:"socks5Proxy,omitempty"`

	// DomainWhitelist limits target hosts. Empty means all domains are allowed.
	// Entries match the exact domain and its subdomains.
	DomainWhitelist []string `json:"domainWhitelist,omitempty"`

	// DisableCompression asks upstream servers for an uncompressed response.
	DisableCompression bool `json:"disableCompression,omitempty"`
}

// Proxy is a configurable reverse proxy handler.
type Proxy struct {
	client             *http.Client
	domainWhitelist    []string
	disableCompression bool
}

// NewProxy creates a reusable proxy handler with explicit configuration.
func NewProxy(config Config) (*Proxy, error) {
	client, err := newHTTPClient(config.Socks5Proxy)
	if err != nil {
		return nil, err
	}
	proxy := &Proxy{
		client:             client,
		domainWhitelist:    normalizeDomainWhitelist(config.DomainWhitelist),
		disableCompression: config.DisableCompression,
	}
	proxy.client.CheckRedirect = proxy.checkRedirect

	return proxy, nil
}

func mustNewProxy(config Config) *Proxy {
	proxy, err := NewProxy(config)
	if err != nil {
		panic(err)
	}
	return proxy
}

func internalServerError(w http.ResponseWriter, err error) {
	if err != nil {
		log.Printf("Internal server error: %v", err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func Handler(w http.ResponseWriter, r *http.Request) {
	defaultProxy.ServeHTTP(w, r)
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if err := recover(); err != nil {
			log.Printf("WithHandler panic: %v", err)
			http.Error(w, fmt.Sprintf("internal server error: %v", err), http.StatusInternalServerError)
		}
	}()

	setCORSHeaders(w)

	// Handle the OPTIONS preflight request
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Redirect to the GitHub repository
	if r.URL.Path == "/" {
		http.Redirect(w, r, githubRepoURL, http.StatusMovedPermanently)
		return
	}

	// Get the URL to proxy
	rawURL := proxyURL(r)
	targetURL, err := parseTargetURL(rawURL)
	if err != nil {
		http.Error(w, "invalid url: "+rawURL, http.StatusBadRequest)
		return
	}
	if err := p.checkDomain(targetURL.Hostname()); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	// Create a new request
	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL.String(), r.Body)
	if err != nil {
		internalServerError(w, err)
		return
	}
	copyHeaders(r.Header, req.Header)
	if p.disableCompression {
		disableUpstreamCompression(req.Header)
	}

	// Send the request to the real server
	resp, err := p.client.Do(req)
	if err != nil {
		var domainErr *domainNotAllowedError
		if errors.As(err, &domainErr) {
			http.Error(w, domainErr.Error(), http.StatusForbidden)
			return
		}
		internalServerError(w, err)
		return
	}
	defer closeResponseBody(resp)

	if err := proxyRaw(w, resp, r); err != nil {
		log.Printf("Proxy response error: %v", err)
	}
}

func proxyRaw(w http.ResponseWriter, resp *http.Response, req *http.Request) error {
	copyHeaders(resp.Header, w.Header())
	if w.Header().Get("Referer") != "" {
		w.Header().Del("Referer")
		w.Header().Add("Referer", req.Host)
	}
	w.WriteHeader(resp.StatusCode)

	// Copy the response body to the output stream
	_, err := io.Copy(w, resp.Body)
	if err != nil {
		return err
	}
	return nil
}

func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", corsAllowOrigin)
	w.Header().Set("Access-Control-Allow-Methods", corsAllowMethods)
	w.Header().Set("Access-Control-Allow-Headers", corsAllowHeaders)
}

func proxyURL(r *http.Request) string {
	u := proxyURLPattern.ReplaceAllString(r.URL.Path, "$1//")
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	return u
}

func parseTargetURL(rawURL string) (*url.URL, error) {
	targetURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if targetURL.Host == "" || (targetURL.Scheme != "http" && targetURL.Scheme != "https") {
		return nil, fmt.Errorf("unsupported target url: %s", rawURL)
	}
	return targetURL, nil
}

func copyHeaders(src, dst http.Header) {
	for k, v := range src {
		for _, vv := range v {
			dst.Add(k, vv)
		}
	}
}

func disableUpstreamCompression(header http.Header) {
	header.Set("Accept-Encoding", identityEncoding)
}

func closeResponseBody(resp *http.Response) {
	if err := resp.Body.Close(); err != nil {
		log.Printf("Close response body error: %v", err)
	}
}

func newHTTPClient(socks5Proxy string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil

	if strings.TrimSpace(socks5Proxy) != "" {
		proxyURL, err := parseSocks5ProxyURL(socks5Proxy)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}

	return &http.Client{Transport: transport}, nil
}

func parseSocks5ProxyURL(rawProxy string) (*url.URL, error) {
	rawProxy = strings.TrimSpace(rawProxy)
	if rawProxy == "" {
		return nil, nil
	}
	if !strings.Contains(rawProxy, "://") {
		rawProxy = "socks5://" + rawProxy
	}

	proxyURL, err := url.Parse(rawProxy)
	if err != nil {
		return nil, fmt.Errorf("invalid socks5 proxy: %w", err)
	}
	proxyURL.Scheme = strings.ToLower(proxyURL.Scheme)
	if proxyURL.Scheme != "socks5" && proxyURL.Scheme != "socks5h" {
		return nil, fmt.Errorf("unsupported proxy scheme %q: only socks5 and socks5h are supported", proxyURL.Scheme)
	}
	if proxyURL.Host == "" {
		return nil, fmt.Errorf("invalid socks5 proxy: missing host")
	}
	return proxyURL, nil
}

func (p *Proxy) isDomainAllowed(host string) bool {
	return isDomainAllowed(host, p.domainWhitelist)
}

func (p *Proxy) checkDomain(host string) error {
	if p.isDomainAllowed(host) {
		return nil
	}
	return &domainNotAllowedError{host: host}
}

func (p *Proxy) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return p.checkDomain(req.URL.Hostname())
}

type domainNotAllowedError struct {
	host string
}

func (e *domainNotAllowedError) Error() string {
	return "domain not allowed: " + e.host
}

func isDomainAllowed(host string, whitelist []string) bool {
	if len(whitelist) == 0 {
		return true
	}

	host = normalizeDomain(host)
	for _, domain := range whitelist {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func normalizeDomainWhitelist(domains []string) []string {
	whitelist := make([]string, 0, len(domains))
	seen := make(map[string]struct{}, len(domains))
	for _, domain := range domains {
		domain = normalizeDomain(domain)
		if domain == "" {
			continue
		}
		if _, ok := seen[domain]; ok {
			continue
		}
		seen[domain] = struct{}{}
		whitelist = append(whitelist, domain)
	}
	return whitelist
}

func normalizeDomain(domain string) string {
	domain = strings.Trim(strings.ToLower(strings.TrimSpace(domain)), ".")
	if domain == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(domain); err == nil {
		domain = strings.Trim(strings.ToLower(host), ".")
	}
	return domain
}
