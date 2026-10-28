package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"loop/pkg/config"
	"loop/pkg/storage"
)

type Credential struct {
	Type         string    `json:"type"`
	Issuer       string    `json:"issuer"`
	Key          string    `json:"key,omitempty"`
	Environment  string    `json:"environment,omitempty"`
	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	ClientID     string    `json:"client_id,omitempty"`
}

type Store struct {
	path   string
	mu     sync.Mutex
	Client *http.Client
}

func NewStore(path string) *Store {
	return &Store{path: path, Client: &http.Client{Timeout: 30 * time.Second}}
}

func (s *Store) read() (map[string]Credential, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return make(map[string]Credential), nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("credential file exceeds 1 MiB")
	}
	var credentials map[string]Credential
	if err := json.Unmarshal(data, &credentials); err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	if credentials == nil {
		credentials = make(map[string]Credential)
	}
	return credentials, nil
}

func (s *Store) Save(name string, credential Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := storage.Lock(s.path+".lock", true)
	if err != nil {
		return err
	}
	defer release()
	credentials, err := s.read()
	if err != nil {
		return err
	}
	credentials[name] = credential
	return s.write(credentials)
}

func (s *Store) write(credentials map[string]Credential) error {
	data, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		return err
	}
	return config.WritePrivateFile(s.path, data)
}

func (s *Store) Logout(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := storage.Lock(s.path+".lock", true)
	if err != nil {
		return err
	}
	defer release()
	credentials, err := s.read()
	if err != nil {
		return err
	}
	delete(credentials, name)
	return s.write(credentials)
}

func (s *Store) Resolve(ctx context.Context, name string, profile config.ProviderConfig) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := storage.Lock(s.path+".lock", true)
	if err != nil {
		return "", err
	}
	defer release()
	credentials, err := s.read()
	if err != nil {
		return "", err
	}
	credential, exists := credentials[name]
	if !exists {
		return profile.ResolvedApiKey(), profile.AuthStatus()
	}
	if credential.Issuer != "" && credential.Issuer != profile.Endpoint {
		return "", fmt.Errorf("credentials belong to a different endpoint; sign in again")
	}
	if credential.Type == "api_key" {
		if credential.Environment != "" {
			value := os.Getenv(credential.Environment)
			if value == "" {
				return "", fmt.Errorf("credential environment variable %s is empty", credential.Environment)
			}
			return value, nil
		}
		return credential.Key, nil
	}
	if credential.Type != "oauth" {
		return "", fmt.Errorf("unsupported credential type %q", credential.Type)
	}
	if !credential.ExpiresAt.IsZero() && time.Until(credential.ExpiresAt) < time.Minute {
		if profile.OAuth == nil || credential.RefreshToken == "" {
			return "", fmt.Errorf("credentials expired; sign in again")
		}
		clientID := profile.OAuth.ClientID
		if credential.ClientID != "" {
			clientID = credential.ClientID
		}
		response, err := s.token(ctx, profile.OAuth.TokenURL, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {credential.RefreshToken}, "client_id": {clientID}, "client_secret": {profile.OAuth.ClientSecret}})
		if err != nil {
			return "", fmt.Errorf("refresh credentials: %w", err)
		}
		credential.AccessToken = response.AccessToken
		if response.RefreshToken != "" {
			credential.RefreshToken = response.RefreshToken
		}
		credential.ExpiresAt = tokenExpiry(response.ExpiresIn)
		credentials[name] = credential
		if err := s.write(credentials); err != nil {
			return "", err
		}
	}
	if credential.AccessToken == "" {
		return "", fmt.Errorf("stored OAuth credential has no access token")
	}
	return credential.AccessToken, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
}

func (s *Store) token(ctx context.Context, endpoint string, values url.Values) (tokenResponse, error) {
	if err := secureURL(endpoint); err != nil {
		return tokenResponse{}, err
	}
	for key, entries := range values {
		if len(entries) == 1 && entries[0] == "" {
			values.Del(key)
		}
	}
	request, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.Client.Do(request)
	if err != nil {
		return tokenResponse{}, err
	}
	defer response.Body.Close()
	var token tokenResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&token); err != nil {
		return token, fmt.Errorf("invalid token response")
	}
	if token.Error != "" {
		return token, fmt.Errorf("authorization server: %s", token.Error)
	}
	if response.StatusCode != http.StatusOK || token.AccessToken == "" {
		return token, fmt.Errorf("token exchange failed (HTTP %d)", response.StatusCode)
	}
	return token, nil
}

func tokenExpiry(seconds int) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Now().Add(time.Duration(seconds) * time.Second)
}

func secureURL(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("invalid authentication endpoint")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1") {
		return nil
	}
	return fmt.Errorf("authentication endpoint must use HTTPS or loopback HTTP")
}
