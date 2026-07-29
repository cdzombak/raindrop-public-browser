package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/cdzombak/raindrop-public-browser/internal/config"
)

// runHealthcheck probes the running server's /_status endpoint and returns
// nil (exit 0) if "up" is true, an error (exit 1) otherwise.
func runHealthcheck() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	host, port, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("cannot parse %s %q: %w", config.EnvListenAddr, cfg.ListenAddr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/_status", net.JoinHostPort(host, port)))
	if err != nil {
		return fmt.Errorf("status endpoint unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status endpoint returned %d", resp.StatusCode)
	}

	var status struct {
		Up bool `json:"up"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return fmt.Errorf("decode status response: %w", err)
	}
	if !status.Up {
		return fmt.Errorf("server reports not up")
	}
	return nil
}
