package diagnostics

import (
	"context"
	"encoding/json"
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
	u.Path = strings.TrimRight(u.Path, "/") + "/models"
	u.RawPath = ""
	request, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		add("Connection", "error", "cannot construct endpoint request", "Check the endpoint address.")
		return report
	}
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range profile.Headers {
		request.Header.Set(k, v)
	}
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
	add("Model", "warning", "configured model was not listed by the server", "Choose an available model with /model.")
	return report
}
