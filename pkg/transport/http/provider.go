package http

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func ApplyProviderHeaders(header http.Header, values map[string]string) {
	for key, value := range values {
		header.Set(key, os.ExpandEnv(value))
	}
}

// Keep protocol-specific authentication identical across inference, discovery,
// and accounting. Callers retain control over custom-header precedence.
func ApplyProviderAuthHeaders(header http.Header, api, key string) {
	switch api {
	case "messages":
		header.Set("x-api-key", key)
		header.Set("anthropic-version", "2023-06-01")
	case "generate-content":
		header.Set("x-goog-api-key", key)
	default:
		if key != "" {
			header.Set("Authorization", "Bearer "+key)
		}
	}
}

// ModelDiscoveryURL accepts either a base API URL or a completion endpoint.
func ModelDiscoveryURL(endpoint, api string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid provider endpoint")
	}
	path := strings.TrimRight(u.Path, "/")
	for _, suffix := range []string{"/chat/completions", "/responses", "/messages", "/models"} {
		if strings.HasSuffix(path, suffix) {
			path = strings.TrimSuffix(path, suffix)
			break
		}
	}
	if !strings.HasSuffix(path, "/v1") && !strings.HasSuffix(path, "/v1beta") {
		if api == "generate-content" {
			path += "/v1beta"
		} else {
			path += "/v1"
		}
	}
	u.Path = path + "/models"
	u.RawPath = ""
	u.Fragment = ""
	return u.String(), nil
}
