package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"loop/pkg/config"
	transporthttp "loop/pkg/transport/http"
)

func (s *Store) Discover(ctx context.Context, resource string) (*config.OAuthConfig, error) {
	u, err := url.Parse(resource)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid MCP resource URL")
	}
	metadataURL := u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource" + strings.TrimRight(u.Path, "/")
	var metadata struct {
		Resource string   `json:"resource"`
		Servers  []string `json:"authorization_servers"`
		Scopes   []string `json:"scopes_supported"`
	}
	if err := s.getJSON(ctx, metadataURL, &metadata); err != nil {
		metadataURL = u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource"
		if err := s.getJSON(ctx, metadataURL, &metadata); err != nil {
			return nil, err
		}
	}
	if metadata.Resource != "" && strings.TrimRight(metadata.Resource, "/") != strings.TrimRight(resource, "/") {
		return nil, fmt.Errorf("OAuth metadata identifies another resource")
	}
	if len(metadata.Servers) == 0 {
		return nil, fmt.Errorf("MCP server advertises no authorization server")
	}
	issuer := strings.TrimRight(metadata.Servers[0], "/")
	if err := secureURL(issuer); err != nil {
		return nil, err
	}
	var authorization struct {
		Issuer           string `json:"issuer"`
		AuthorizationURL string `json:"authorization_endpoint"`
		TokenURL         string `json:"token_endpoint"`
		RegistrationURL  string `json:"registration_endpoint"`
		DeviceURL        string `json:"device_authorization_endpoint"`
	}
	issuerURL, err := url.Parse(issuer)
	if err != nil {
		return nil, err
	}
	endpoint := issuerURL.Scheme + "://" + issuerURL.Host + "/.well-known/oauth-authorization-server" + issuerURL.Path
	if err := s.getJSON(ctx, endpoint, &authorization); err != nil {
		if err := s.getJSON(ctx, issuer+"/.well-known/openid-configuration", &authorization); err != nil {
			return nil, err
		}
	}
	if strings.TrimRight(authorization.Issuer, "/") != issuer {
		return nil, fmt.Errorf("authorization issuer does not match discovery metadata")
	}
	return &config.OAuthConfig{AuthorizationURL: authorization.AuthorizationURL, TokenURL: authorization.TokenURL, RegistrationURL: authorization.RegistrationURL, DeviceURL: authorization.DeviceURL, Scopes: metadata.Scopes}, nil
}

func (s *Store) getJSON(ctx context.Context, endpoint string, destination any) error {
	if err := secureURL(endpoint); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return err
	}
	response, err := s.Client.Do(request)
	if err != nil {
		return transporthttp.RedactError(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("authentication discovery failed (HTTP %d)", response.StatusCode)
	}
	return decodeResponse(response.Body, destination)
}
