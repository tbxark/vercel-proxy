package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestProxyURLKeepsExistingFormats(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "full https url",
			path: "/https://example.com/path?param=value",
			want: "https://example.com/path?param=value",
		},
		{
			name: "single slash https url",
			path: "/https:/example.com/path?param=value",
			want: "https://example.com/path?param=value",
		},
		{
			name: "leading slashes http url",
			path: "///http:/example.com/path?param=value",
			want: "http://example.com/path?param=value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if got := proxyURL(req); got != tt.want {
				t.Fatalf("proxyURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConfigUnmarshalsFromJSON(t *testing.T) {
	data := []byte(`{
		"socks5Proxy": "127.0.0.1:1080",
		"domainWhitelist": ["example.com", "api.example.org"],
		"disableCompression": true,
		"disableGlobalCors": true
	}`)

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if config.Socks5Proxy != "127.0.0.1:1080" {
		t.Fatalf("Socks5Proxy = %q, want %q", config.Socks5Proxy, "127.0.0.1:1080")
	}
	if len(config.DomainWhitelist) != 2 || config.DomainWhitelist[0] != "example.com" || config.DomainWhitelist[1] != "api.example.org" {
		t.Fatalf("DomainWhitelist = %#v, want %#v", config.DomainWhitelist, []string{"example.com", "api.example.org"})
	}
	if !config.DisableCompression {
		t.Fatal("DisableCompression = false, want true")
	}
	if !config.DisableGlobalCORS {
		t.Fatal("DisableGlobalCORS = false, want true")
	}
}

func TestProxyHandlesPreflightByDefault(t *testing.T) {
	proxy, err := NewProxy(Config{})
	if err != nil {
		t.Fatalf("NewProxy() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodOptions, "/https://example.com", nil)
	recorder := httptest.NewRecorder()

	proxy.ServeHTTP(recorder, req)

	resp := recorder.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != corsAllowOrigin {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, corsAllowOrigin)
	}
}

func TestHandlerRedirectsRoot(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()

	Handler(recorder, req)

	resp := recorder.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusMovedPermanently)
	}
	if got := resp.Header.Get("Location"); got != githubRepoURL {
		t.Fatalf("Location = %q, want %q", got, githubRepoURL)
	}
}

func TestHandlerProxiesResponse(t *testing.T) {
	upstreamErr := make(chan error, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			upstreamErr <- fmt.Errorf("Method = %q, want %q", r.Method, http.MethodPost)
			return
		}
		if r.URL.RawQuery != "param=value" {
			upstreamErr <- fmt.Errorf("RawQuery = %q, want %q", r.URL.RawQuery, "param=value")
			return
		}
		if got := r.Header.Get("X-Test-Header"); got != "client-value" {
			upstreamErr <- fmt.Errorf("X-Test-Header = %q, want %q", got, "client-value")
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			upstreamErr <- fmt.Errorf("ReadAll() error = %w", err)
			return
		}
		if string(body) != "request-body" {
			upstreamErr <- fmt.Errorf("body = %q, want %q", body, "request-body")
			return
		}
		upstreamErr <- nil

		w.Header().Set("X-Upstream-Header", "upstream-value")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("response-body"))
	}))
	defer upstream.Close()

	req := httptest.NewRequest(http.MethodPost, "/"+upstream.URL+"?param=value", strings.NewReader("request-body"))
	req.Header.Set("X-Test-Header", "client-value")
	recorder := httptest.NewRecorder()

	Handler(recorder, req)
	assertUpstreamErr(t, upstreamErr)

	resp := recorder.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusTeapot)
	}
	if got := resp.Header.Get("X-Upstream-Header"); got != "upstream-value" {
		t.Fatalf("X-Upstream-Header = %q, want %q", got, "upstream-value")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(body) != "response-body" {
		t.Fatalf("body = %q, want %q", body, "response-body")
	}
}

func TestProxyOverridesCORSHeadersByDefault(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "https://upstream.example")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Expose-Headers", "X-Upstream-Header")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	proxy, err := NewProxy(Config{})
	if err != nil {
		t.Fatalf("NewProxy() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/"+upstream.URL, nil)
	recorder := httptest.NewRecorder()

	proxy.ServeHTTP(recorder, req)

	resp := recorder.Result()
	defer resp.Body.Close()

	if got := resp.Header.Values("Access-Control-Allow-Origin"); len(got) != 1 || got[0] != corsAllowOrigin {
		t.Fatalf("Access-Control-Allow-Origin values = %#v, want %#v", got, []string{corsAllowOrigin})
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); got != corsAllowMethods {
		t.Fatalf("Access-Control-Allow-Methods = %q, want %q", got, corsAllowMethods)
	}
	if got := resp.Header.Get("Access-Control-Allow-Headers"); got != corsAllowHeaders {
		t.Fatalf("Access-Control-Allow-Headers = %q, want %q", got, corsAllowHeaders)
	}
	if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("Access-Control-Allow-Credentials = %q, want empty", got)
	}
	if got := resp.Header.Get("Access-Control-Expose-Headers"); got != "" {
		t.Fatalf("Access-Control-Expose-Headers = %q, want empty", got)
	}
}

func TestProxyKeepsCORSHeadersWhenGlobalCORSDisabled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "https://upstream.example")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	proxy, err := NewProxy(Config{DisableGlobalCORS: true})
	if err != nil {
		t.Fatalf("NewProxy() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/"+upstream.URL, nil)
	recorder := httptest.NewRecorder()

	proxy.ServeHTTP(recorder, req)

	resp := recorder.Result()
	defer resp.Body.Close()

	if got := resp.Header.Values("Access-Control-Allow-Origin"); len(got) != 1 || got[0] != "https://upstream.example" {
		t.Fatalf("Access-Control-Allow-Origin values = %#v, want %#v", got, []string{"https://upstream.example"})
	}
	if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("Access-Control-Allow-Credentials = %q, want true", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); got != "" {
		t.Fatalf("Access-Control-Allow-Methods = %q, want empty", got)
	}
}

func TestProxyDisablesUpstreamCompressionWhenConfigured(t *testing.T) {
	proxy, err := NewProxy(Config{DisableCompression: true})
	if err != nil {
		t.Fatalf("NewProxy() error = %v", err)
	}

	acceptEncoding := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptEncoding <- r.Header.Get("Accept-Encoding")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	req := httptest.NewRequest(http.MethodGet, "/"+upstream.URL, nil)
	req.Header.Set("Accept-Encoding", "br, gzip, deflate")
	recorder := httptest.NewRecorder()

	proxy.ServeHTTP(recorder, req)

	select {
	case got := <-acceptEncoding:
		if got != identityEncoding {
			t.Fatalf("Accept-Encoding = %q, want %q", got, identityEncoding)
		}
	default:
		t.Fatal("upstream was not called")
	}
}

func TestNewProxyConfiguresSocks5Proxy(t *testing.T) {
	proxy, err := NewProxy(Config{Socks5Proxy: "127.0.0.1:1080"})
	if err != nil {
		t.Fatalf("NewProxy() error = %v", err)
	}

	transport, ok := proxy.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", proxy.client.Transport)
	}
	if transport.Proxy == nil {
		t.Fatal("Transport.Proxy is nil")
	}

	proxyURL, err := transport.Proxy(httptest.NewRequest(http.MethodGet, "https://example.com", nil))
	if err != nil {
		t.Fatalf("Transport.Proxy() error = %v", err)
	}
	if proxyURL.Scheme != "socks5" || proxyURL.Host != "127.0.0.1:1080" {
		t.Fatalf("proxy URL = %q, want socks5://127.0.0.1:1080", proxyURL.String())
	}
}

func TestNewProxyRejectsUnsupportedProxyScheme(t *testing.T) {
	_, err := NewProxy(Config{Socks5Proxy: "http://127.0.0.1:8080"})
	if err == nil {
		t.Fatal("NewProxy() error = nil, want error")
	}
}

func TestDomainWhitelistAllowsExactAndSubdomain(t *testing.T) {
	whitelist := normalizeDomainWhitelist([]string{"Example.COM"})

	tests := []struct {
		host string
		want bool
	}{
		{host: "example.com", want: true},
		{host: "api.example.com", want: true},
		{host: "badexample.com", want: false},
		{host: "example.org", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := isDomainAllowed(tt.host, whitelist); got != tt.want {
				t.Fatalf("isDomainAllowed(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestDomainWhitelistSupportsWildcardAndExcludeRules(t *testing.T) {
	whitelist := normalizeDomainWhitelist([]string{"*.example.com", "-private.example.com", "api?.example.org"})

	tests := []struct {
		host string
		want bool
	}{
		{host: "api.example.com", want: true},
		{host: "nested.api.example.com", want: true},
		{host: "private.example.com", want: false},
		{host: "example.com", want: false},
		{host: "api1.example.org", want: true},
		{host: "api12.example.org", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := isDomainAllowed(tt.host, whitelist); got != tt.want {
				t.Fatalf("isDomainAllowed(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestDomainWhitelistSupportsPortRules(t *testing.T) {
	whitelist := normalizeDomainWhitelist([]string{"example.com", "api.example.com:8443", "static.example.com:0"})

	tests := []struct {
		rawURL string
		want   bool
	}{
		{rawURL: "https://example.com/path", want: true},
		{rawURL: "https://example.com:443/path", want: true},
		{rawURL: "https://example.com:8443/path", want: false},
		{rawURL: "https://api.example.com:8443/path", want: true},
		{rawURL: "https://api.example.com:9443/path", want: false},
		{rawURL: "https://static.example.com:9443/path", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.rawURL, func(t *testing.T) {
			targetURL, err := url.Parse(tt.rawURL)
			if err != nil {
				t.Fatalf("url.Parse() error = %v", err)
			}
			if got := isDomainURLAllowed(targetURL, whitelist); got != tt.want {
				t.Fatalf("isDomainURLAllowed(%q) = %v, want %v", tt.rawURL, got, tt.want)
			}
		})
	}
}

func TestProxyRejectsDomainOutsideWhitelist(t *testing.T) {
	proxy, err := NewProxy(Config{DomainWhitelist: []string{"example.com"}})
	if err != nil {
		t.Fatalf("NewProxy() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/https://blocked.example.org/path", nil)
	recorder := httptest.NewRecorder()

	proxy.ServeHTTP(recorder, req)

	resp := recorder.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}

func TestProxyRejectsRedirectOutsideWhitelist(t *testing.T) {
	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://blocked.invalid/path", http.StatusFound)
	}))
	defer allowed.Close()

	allowedURL, err := url.Parse(allowed.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	proxy, err := NewProxy(Config{DomainWhitelist: []string{allowedURL.Host}})
	if err != nil {
		t.Fatalf("NewProxy() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/"+allowed.URL, nil)
	recorder := httptest.NewRecorder()

	proxy.ServeHTTP(recorder, req)

	resp := recorder.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}

func assertUpstreamErr(t *testing.T, upstreamErr <-chan error) {
	t.Helper()

	select {
	case err := <-upstreamErr:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("upstream was not called")
	}
}
