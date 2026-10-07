package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Used by the installer; never imports a document or restarts xochitl.
func checkInstallation(token string) error {
	client := &http.Client{Timeout: 45 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	ready := false
	for attempt := 0; attempt < 10; attempt++ {
		if err := checkEndpoint(client, "http://127.0.0.1:8765/health", token); err == nil {
			ready = true
			break
		}
		time.Sleep(time.Second)
	}
	if !ready {
		return fmt.Errorf("Web Inbox did not become ready; inspect: journalctl -u web-inbox.service -n 30")
	}
	if err := checkEndpoint(client, "http://127.0.0.1:8765/health/importer", token); err != nil {
		return fmt.Errorf("native importer is not ready: %w; enable USB web interface and exit takeover apps", err)
	}
	return nil
}

func checkEndpoint(client *http.Client, address, token string) error {
	req, err := http.NewRequest(http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Web-Inbox-Key", token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(data)) != "ok" {
		return fmt.Errorf("health check returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}
