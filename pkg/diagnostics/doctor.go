package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"loop/pkg/auth"
	"loop/pkg/config"
	transporthttp "loop/pkg/transport/http"
)

type Check struct{ Name, Status, Detail, Next string }
type Report struct{ Checks []Check }

func (r Report) Failed() bool {
	for _, c := range r.Checks {
		if c.Status == "error" {
			return true
		}
	}
	return false
}
func (r Report) Write(w io.Writer) {
	fmt.Fprintln(w, "Loop health check")
	for _, c := range r.Checks {
		mark := "✓"
		if c.Status == "warning" {
			mark = "!"
		}
		if c.Status == "error" {
			mark = "✗"
		}
		fmt.Fprintf(w, "  %s %s: %s\n", mark, c.Name, c.Detail)
		if c.Next != "" {
			fmt.Fprintf(w, "    Next: %s\n", c.Next)
		}
	}
}

// Run inspects configuration and optionally queries model discovery. It never
// runs inference, tools or project hooks and never prints credential values.
func Run(ctx context.Context, cfg *config.Config, path, workspace string, offline bool) Report {
	var report Report
	add := func(name, status, detail, next string) {
		report.Checks = append(report.Checks, Check{name, status, detail, next})
	}
	add("Workspace", "ok", workspace, "")
	if cfg == nil {
		add("Settings", "error", "configuration unavailable", "Check the config file.")
		return report
	}
	if err := cfg.ValidateSettings(); err != nil {
		add("Settings", "error", err.Error(), "Correct the invalid configuration value.")
		return report
	}
	for _, directory := range []string{filepath.Dir(path), filepath.Join(filepath.Dir(path), "sessions")} {
		if err := checkStorageDirectory(directory); err != nil {
			add("Storage", "error", "cannot write and sync "+directory, "Check permissions and available disk space.")
		} else {
			add("Storage", "ok", directory+" is writable", "")
		}
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		add("Endpoint", "error", "invalid endpoint", "Set an HTTP or HTTPS endpoint with /provider.")
		return report
	}
	add("Endpoint", "ok", u.Scheme+"://"+u.Host+u.Path, "")
	for _, name := range []string{"bash", "git"} {
		if _, err := exec.LookPath(name); err != nil {
			add(name, "warning", "not found on PATH", "Install "+name+" to use its related tools.")
		} else {
			add(name, "ok", "available", "")
		}
	}
	terminal := os.Getenv("TERM")
	if terminal == "" {
		terminal = "not reported"
	}
	if os.Getenv("TMUX") != "" {
		terminal += " · tmux"
	}
	if os.Getenv("SSH_CONNECTION") != "" {
		terminal += " · SSH"
	}
	add("Terminal", "ok", terminal, "")
	tls, err := config.GetTLSConfig(cfg)
	if err != nil {
		add("TLS", "error", err.Error(), "Check ca_file, cert_file and key_file.")
		return report
	}
	profile := cfg.Providers[cfg.ActiveProvider]
	profile.Endpoint = cfg.Endpoint
	profile.APIKey = cfg.APIKey
	if offline {
		if err := profile.AuthStatus(); err != nil {
			add("Authentication", "error", err.Error(), "Set the referenced environment variable.")
		} else {
			add("Authentication", "ok", "settings checked; sign-in not contacted", "")
		}
		add("Connection", "warning", "network checks skipped", "Run doctor without --offline to check the endpoint.")
		return report
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	key, err := auth.NewStore(filepath.Join(filepath.Dir(path), "auth.json")).Resolve(ctx, cfg.ActiveProvider, profile)
	if err != nil {
		add("Authentication", "error", err.Error(), "Check your provider credentials or sign in again.")
		return report
	}
	endpoint, err := transporthttp.ModelDiscoveryURL(cfg.Endpoint, profile.API)
	if err != nil {
		add("Connection", "error", "invalid model discovery URL", "Check the endpoint address.")
		return report
	}
	request, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		add("Connection", "error", "cannot construct endpoint request", "Check the endpoint address.")
		return report
	}
	transporthttp.ApplyProviderAuthHeaders(request.Header, profile.API, key)
	transporthttp.ApplyProviderHeaders(request.Header, profile.Headers)
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if base, ok := http.DefaultTransport.(*http.Transport); ok && base != nil {
		transport = base.Clone()
	}
	transport.TLSClientConfig = tls
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		detail := "could not reach the endpoint"
		if ctx.Err() != nil {
			detail = "connection timed out or was cancelled"
		}
		add("Connection", "error", detail, "Check that the model server is running and the endpoint is correct.")
		return report
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case 401, 403:
		add("Authentication", "error", fmt.Sprintf("endpoint rejected credentials (HTTP %d)", response.StatusCode), "Check your API key or sign in again.")
		return report
	case 404, 405:
		add("Connection", "ok", "endpoint reachable", "")
		add("Models", "warning", "provider does not offer model discovery", "Check the configured model in /model.")
		return report
	case 200:
	default:
		add("Connection", "error", fmt.Sprintf("endpoint returned HTTP %d", response.StatusCode), "Check the server status and endpoint path.")
		return report
	}
	add("Connection", "ok", "endpoint reachable", "")
	add("Authentication", "ok", "model discovery accepted", "")
	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&models); err != nil {
		add("Models", "warning", "discovery response could not be read", "Check that the endpoint points to an API, not a webpage.")
		return report
	}
	for _, m := range models.Data {
		if m.ID == cfg.Model {
			add("Model", "ok", cfg.Model, "")
			return report
		}
	}
	for _, m := range models.Models {
		if strings.TrimPrefix(m.Name, "models/") == strings.TrimPrefix(cfg.Model, "models/") {
			add("Model", "ok", cfg.Model, "")
			return report
		}
	}
	add("Model", "warning", "configured model was not listed by the server", "Choose an available model with /model.")
	return report
}

func checkStorageDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".loop-doctor-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.WriteString("loop storage check\n")
	return errors.Join(writeErr, file.Sync(), file.Close())
}
