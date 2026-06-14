package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigReturnsDefaultWhenPathIsEmpty(t *testing.T) {
	config, err := loadConfig("")
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	if config.Socks5Proxy != "" {
		t.Fatalf("Socks5Proxy = %q, want empty", config.Socks5Proxy)
	}
	if len(config.DomainWhitelist) != 0 {
		t.Fatalf("DomainWhitelist = %#v, want empty", config.DomainWhitelist)
	}
	if config.DisableCompression {
		t.Fatal("DisableCompression = true, want false")
	}
}

func TestLoadConfigReadsJSONFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{
		"socks5Proxy": "socks5://127.0.0.1:1080",
		"domainWhitelist": ["example.com"],
		"disableCompression": true
	}`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	config, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	if config.Socks5Proxy != "socks5://127.0.0.1:1080" {
		t.Fatalf("Socks5Proxy = %q, want %q", config.Socks5Proxy, "socks5://127.0.0.1:1080")
	}
	if len(config.DomainWhitelist) != 1 || config.DomainWhitelist[0] != "example.com" {
		t.Fatalf("DomainWhitelist = %#v, want %#v", config.DomainWhitelist, []string{"example.com"})
	}
	if !config.DisableCompression {
		t.Fatal("DisableCompression = false, want true")
	}
}
