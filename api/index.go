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
	// Entries match the exact domain and its subdomains. Wildcard characters * and ? are supported.
	// Prefix an entry with - to exclude it. Suffix :port limits the entry to that port; :0 allows any port.
	DomainWhitelist []string `json:"domainWhitelist,omitempty"`

	// DisableCompression asks upstream servers for an uncompressed response.
	DisableCompression bool `json:"disableCompression,omitempty"`

	// DisableGlobalCORS disables proxy-managed CORS headers for all responses.
	DisableGlobalCORS bool `json:"disableGlobalCors,omitempty"`
}

// Proxy is a configurable reverse proxy handler.
type Proxy struct {
	client             *http.Client
	domainWhitelist    []domainRule
	disableCompression bool
	globalCORS         bool
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
		globalCORS:         !config.DisableGlobalCORS,
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

	if p.globalCORS {
		setCORSHeaders(w)

		// Handle the OPTIONS preflight request
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
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
	if err := p.checkDomain(targetURL); err != nil {
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

	if err := proxyRaw(w, resp, r, p.globalCORS); err != nil {
		log.Printf("Proxy response error: %v", err)
	}
}

func proxyRaw(w http.ResponseWriter, resp *http.Response, req *http.Request, globalCORS bool) error {
	copyHeaders(resp.Header, w.Header())
	if globalCORS {
		setCORSHeaders(w)
	}
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
	clearCORSHeaders(w.Header())
	w.Header().Set("Access-Control-Allow-Origin", corsAllowOrigin)
	w.Header().Set("Access-Control-Allow-Methods", corsAllowMethods)
	w.Header().Set("Access-Control-Allow-Headers", corsAllowHeaders)
}

func clearCORSHeaders(header http.Header) {
	for k := range header {
		if strings.HasPrefix(strings.ToLower(k), "access-control-") {
			header.Del(k)
		}
	}
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

func (p *Proxy) isDomainAllowed(targetURL *url.URL) bool {
	return isDomainURLAllowed(targetURL, p.domainWhitelist)
}

func (p *Proxy) checkDomain(targetURL *url.URL) error {
	if p.isDomainAllowed(targetURL) {
		return nil
	}
	return &domainNotAllowedError{host: targetURL.Hostname()}
}

func (p *Proxy) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return p.checkDomain(req.URL)
}

type domainNotAllowedError struct {
	host string
}

func (e *domainNotAllowedError) Error() string {
	return "domain not allowed: " + e.host
}

type domainRule struct {
	exclude bool
	pattern string
	regexp  *regexp.Regexp
	port    string
}

func isDomainAllowed(host string, whitelist []domainRule) bool {
	target := normalizeTargetHostPort(host, "")
	return isDomainTargetAllowed(target, whitelist)
}

func isDomainURLAllowed(targetURL *url.URL, whitelist []domainRule) bool {
	target := normalizeTargetHostPort(targetURL.Host, targetURL.Scheme)
	return isDomainTargetAllowed(target, whitelist)
}

func isDomainTargetAllowed(target domainTarget, whitelist []domainRule) bool {
	if len(whitelist) == 0 {
		return true
	}

	allowed := false
	for _, rule := range whitelist {
		if !rule.matches(target) {
			continue
		}
		if rule.exclude {
			return false
		}
		allowed = true
	}
	return allowed
}

func normalizeDomainWhitelist(entries []string) []domainRule {
	whitelist := make([]domainRule, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		rule, ok := normalizeDomainRule(entry)
		if !ok {
			continue
		}
		key := rule.key()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		whitelist = append(whitelist, rule)
	}
	return whitelist
}

func normalizeDomainRule(entry string) (domainRule, bool) {
	entry = strings.TrimSpace(entry)
	exclude := strings.HasPrefix(entry, "-")
	if exclude {
		entry = strings.TrimSpace(strings.TrimPrefix(entry, "-"))
	}

	host, port := splitDomainRulePort(entry)
	host = normalizeDomain(host)
	if host == "" {
		return domainRule{}, false
	}

	rule := domainRule{exclude: exclude, pattern: host, port: port}
	if strings.ContainsAny(host, "*?") {
		rule.regexp = compileDomainWildcard(host)
	}
	return rule, true
}

func splitDomainRulePort(entry string) (string, string) {
	if host, port, err := net.SplitHostPort(entry); err == nil {
		return host, normalizePort(port)
	}
	if strings.Count(entry, ":") == 1 {
		idx := strings.LastIndex(entry, ":")
		port := normalizePort(entry[idx+1:])
		if port != "" {
			return entry[:idx], port
		}
	}
	return entry, ""
}

func normalizePort(port string) string {
	port = strings.TrimSpace(port)
	if port == "" {
		return ""
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return port
}

func compileDomainWildcard(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

func (r domainRule) key() string {
	if r.exclude {
		return "-" + r.pattern + ":" + r.port
	}
	return r.pattern + ":" + r.port
}

func (r domainRule) matches(target domainTarget) bool {
	if !r.matchesHost(target.host) {
		return false
	}
	return r.matchesPort(target.port)
}

func (r domainRule) matchesHost(host string) bool {
	if r.regexp != nil {
		return r.regexp.MatchString(host)
	}
	return host == r.pattern || strings.HasSuffix(host, "."+r.pattern)
}

func (r domainRule) matchesPort(port string) bool {
	if r.port == "0" {
		return true
	}
	if r.port != "" {
		return port == r.port
	}
	return port == "" || port == "80" || port == "443"
}

type domainTarget struct {
	host string
	port string
}

func normalizeTargetHostPort(rawHost, scheme string) domainTarget {
	host := rawHost
	port := ""
	if parsedHost, parsedPort, err := net.SplitHostPort(rawHost); err == nil {
		host = parsedHost
		port = parsedPort
	} else if strings.Count(rawHost, ":") == 1 {
		idx := strings.LastIndex(rawHost, ":")
		if parsedPort := normalizePort(rawHost[idx+1:]); parsedPort != "" {
			host = rawHost[:idx]
			port = parsedPort
		}
	}
	if port == "" {
		port = defaultPort(scheme)
	}
	return domainTarget{host: normalizeDomain(host), port: port}
}

func defaultPort(scheme string) string {
	switch strings.ToLower(scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func normalizeDomain(domain string) string {
	domain = strings.Trim(strings.ToLower(strings.TrimSpace(domain)), ".")
	domain = strings.TrimPrefix(strings.TrimSuffix(domain, "]"), "[")
	if domain == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(domain); err == nil {
		domain = strings.Trim(strings.ToLower(host), ".")
	}
	return domain
}
