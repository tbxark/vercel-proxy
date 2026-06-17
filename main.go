package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/tbxark/vercel-proxy/api"
)

// BuildVersion is set at build time via -ldflags.
var BuildVersion = "dev"

func main() {
	addr := flag.String("addr", ":3000", "address to listen on")
	configPath := flag.String("config", "", "path to JSON config file")
	flag.Parse()

	config, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
		return
	}

	proxy, err := api.NewProxy(config)
	if err != nil {
		log.Fatalf("Failed to create proxy: %v", err)
		return
	}

	err = http.ListenAndServe(*addr, proxy)
	if err != nil {
		log.Fatalf("Failed to start server: %v", err)
		return
	}
}

func loadConfig(path string) (api.Config, error) {
	if path == "" {
		return api.Config{}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return api.Config{}, err
	}

	var config api.Config
	if err := json.Unmarshal(data, &config); err != nil {
		return api.Config{}, err
	}
	return config, nil
}
