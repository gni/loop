package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"loop/pkg/config"
	"loop/pkg/storage"
	transporthttp "loop/pkg/transport/http"
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

const credentialCommitTimeout = 5 * time.Second

type credentialRefresh struct {
	done  chan struct{}
	key   string
	err   error
	retry bool
}

type refreshRequestCanceled struct{ cause error }

func (e *refreshRequestCanceled) Error() string { return e.cause.Error() }
func (e *refreshRequestCanceled) Unwrap() error { return e.cause }

type Store struct {
	refreshing  map[string]*credentialRefresh
	path        string
	mu          sync.Mutex
	Client      *http.Client
	cached      bool
	cacheExists bool
	cacheMod    time.Time
	cacheSize   int64
	cacheCreds  map[string]Credential
}

var (
	sharedStoresMu sync.Mutex
	sharedStores   = make(map[string]*Store)
)

func newSecureHTTPClient() *http.Client {
	var transport http.RoundTripper
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many authentication redirects")
			}
			if err := secureURL(req.URL.String()); err != nil {
				return err
			}
			if len(via) > 0 && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
				return fmt.Errorf("cross-host authentication redirect refused")
			}
			return nil
		},
	}
}

func NewStore(path string) *Store {
	sharedStoresMu.Lock()
	defer sharedStoresMu.Unlock()
	if existing, ok := sharedStores[path]; ok {
		return existing
	}
	store := &Store{path: path, Client: newSecureHTTPClient()}
	sharedStores[path] = store
	return store
}

func cloneCredentialMap(src map[string]Credential) map[string]Credential {
	out := make(map[string]Credential, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func (s *Store) read() (map[string]Credential, error) {
	info, statErr := os.Stat(s.path)
	if os.IsNotExist(statErr) {
		empty := make(map[string]Credential)
		s.cached = true
		s.cacheExists = false
		s.cacheMod = time.Time{}
		s.cacheSize = 0
		s.cacheCreds = empty
		return cloneCredentialMap(empty), nil
	}
	if statErr != nil {
		return nil, statErr
	}
	if info.Size() > 1<<20 {
		return nil, fmt.Errorf("credential file exceeds 1 MiB")
	}
	data, err := storage.ReadFile(s.path, 1<<20)
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
	s.cached = true
	s.cacheExists = true
	s.cacheMod = info.ModTime()
	s.cacheSize = info.Size()
	s.cacheCreds = cloneCredentialMap(credentials)
	return credentials, nil
}

// Never hold the store mutex while waiting for another process's file lock.
// When both are needed, the file lock is always acquired first.
// On success, the caller owns s.mu and must unlock it.
func (s *Store) lockCredentials(ctx context.Context) (map[string]Credential, error) {
	if err := s.lock(ctx); err != nil {
		return nil, err
	}
	info, statErr := os.Stat(s.path)
	if os.IsNotExist(statErr) && s.cached && !s.cacheExists {
		credentials := cloneCredentialMap(s.cacheCreds)
		return credentials, nil
	}
	if statErr == nil && s.cached && s.cacheExists && info.ModTime().Equal(s.cacheMod) && info.Size() == s.cacheSize {
		credentials := cloneCredentialMap(s.cacheCreds)
		return credentials, nil
	}
	s.mu.Unlock()
	release, err := storage.LockContext(ctx, s.path+".lock", false)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := s.lock(ctx); err != nil {
		return nil, err
	}
	credentials, err := s.read()
	if err != nil {
		s.mu.Unlock()
	}
	return credentials, err
}

func (s *Store) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ctx.Done() == nil {
		s.mu.Lock()
		return nil
	}
	for !s.mu.TryLock() {
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *Store) Save(name string, credential Credential) error {
	return s.SaveContext(context.Background(), name, credential)
}

// SaveContext stores credentials while allowing cancellation of lock waits.
func (s *Store) SaveContext(ctx context.Context, name string, credential Credential) error {
	release, err := storage.LockContext(ctx, s.path+".lock", true)
	if err != nil {
		return err
	}
	defer release()
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
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
	if len(data) > 1<<20 {
		return fmt.Errorf("credential file exceeds 1 MiB")
	}
	if err := config.WritePrivateFile(s.path, data); err != nil {
		s.cached = false
		return err
	}
	if info, statErr := os.Stat(s.path); statErr == nil {
		s.cached = true
		s.cacheExists = true
		s.cacheMod = info.ModTime()
		s.cacheSize = info.Size()
		s.cacheCreds = cloneCredentialMap(credentials)
	} else {
		s.cached = false
	}
	return nil
}

func (s *Store) Logout(name string) error { return s.LogoutContext(context.Background(), name) }

// LogoutContext removes credentials while allowing cancellation of lock waits.
func (s *Store) LogoutContext(ctx context.Context, name string) error {
	release, err := storage.LockContext(ctx, s.path+".lock", true)
	if err != nil {
		return err
	}
	defer release()
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	credentials, err := s.read()
	if err != nil {
		return err
	}
	delete(credentials, name)
	return s.write(credentials)
}

func (s *Store) Resolve(ctx context.Context, name string, profile config.ProviderConfig) (string, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		credentials, err := s.lockCredentials(ctx)
		if err != nil {
			return "", err
		}
		credential, exists := credentials[name]
		if !exists {
			s.mu.Unlock()
			return profile.ResolvedAPIKey(), profile.AuthStatus()
		}
		if credential.Issuer != "" && credential.Issuer != profile.Endpoint {
			s.mu.Unlock()
			return "", fmt.Errorf("credentials belong to a different endpoint; sign in again")
		}
		if credential.Type == "api_key" {
			s.mu.Unlock()
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
			s.mu.Unlock()
			return "", fmt.Errorf("unsupported credential type %q", credential.Type)
		}
		needsRefresh := !credential.ExpiresAt.IsZero() && time.Until(credential.ExpiresAt) < time.Minute
		if !needsRefresh {
			s.mu.Unlock()
			if credential.AccessToken == "" {
				return "", fmt.Errorf("stored OAuth credential has no access token")
			}
			return credential.AccessToken, nil
		}
		if profile.OAuth == nil || credential.RefreshToken == "" {
			s.mu.Unlock()
			return "", fmt.Errorf("credentials expired; sign in again")
		}
		attempt := s.refreshing[name]
		start := attempt == nil
		if start {
			if s.refreshing == nil {
				s.refreshing = make(map[string]*credentialRefresh)
			}
			attempt = &credentialRefresh{done: make(chan struct{})}
			s.refreshing[name] = attempt
		}
		s.mu.Unlock()
		if start {
			// The worker can outlive this caller; own its OAuth settings.
			oauth := *profile.OAuth
			oauth.Scopes = append([]string(nil), profile.OAuth.Scopes...)
			profile.OAuth = &oauth
			go s.runCredentialRefresh(ctx, name, profile, credential, attempt)
		}
		select {
		case <-attempt.done:
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if attempt.retry {
				continue
			}
			return attempt.key, attempt.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func (s *Store) runCredentialRefresh(ctx context.Context, name string, profile config.ProviderConfig, credential Credential, attempt *credentialRefresh) {
	key, err := s.refreshCredential(ctx, name, profile, credential)
	s.mu.Lock()
	attempt.key, attempt.err = key, err
	var canceled *refreshRequestCanceled
	attempt.retry = errors.As(err, &canceled)
	delete(s.refreshing, name)
	close(attempt.done)
	s.mu.Unlock()
}

func (s *Store) refreshCredential(ctx context.Context, name string, profile config.ProviderConfig, credential Credential) (string, error) {
	clientID := profile.OAuth.ClientID
	if credential.ClientID != "" {
		clientID = credential.ClientID
	}
	refreshToken := credential.RefreshToken

	response, err := s.token(ctx, profile.OAuth.TokenURL, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
		"client_secret": {profile.OAuth.ClientSecret},
	})
	if err != nil {
		if ctx.Err() != nil {
			return "", &refreshRequestCanceled{cause: ctx.Err()}
		}
		return "", fmt.Errorf("refresh credentials: %w", err)
	}

	// A successful exchange may rotate the refresh token. Give its durable
	// commit a short independent budget even if the initiating caller leaves.
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), credentialCommitTimeout)
	defer cancel()
	release, err := storage.LockContext(commitCtx, s.path+".lock", true)
	if err != nil {
		return "", fmt.Errorf("save refreshed credentials: %w", err)
	}
	defer release()
	if err := s.lock(commitCtx); err != nil {
		return "", fmt.Errorf("save refreshed credentials: %w", err)
	}
	defer s.mu.Unlock()
	latest, err := s.read()
	if err != nil {
		return "", err
	}
	current, exists := latest[name]
	if !exists || !sameCredential(current, credential) {
		return "", fmt.Errorf("credentials changed during refresh; retry authentication")
	}
	updated := current
	updated.AccessToken = response.AccessToken
	if response.RefreshToken != "" {
		updated.RefreshToken = response.RefreshToken
	}
	updated.ExpiresAt = tokenExpiry(response.ExpiresIn)
	latest[name] = updated
	if err := s.write(latest); err != nil {
		return "", fmt.Errorf("save refreshed credentials: %w", err)
	}
	if updated.AccessToken == "" {
		return "", fmt.Errorf("stored OAuth credential has no access token")
	}
	return updated.AccessToken, nil
}

// Compare the snapshot without time's in-memory monotonic metadata: the latest
// value has just been decoded from disk. A logout or replacement wins over an
// older refresh response, including a refresh from another process.
func sameCredential(a, b Credential) bool {
	return a.Type == b.Type && a.Issuer == b.Issuer && a.Key == b.Key && a.Environment == b.Environment && a.AccessToken == b.AccessToken && a.RefreshToken == b.RefreshToken && a.ClientID == b.ClientID && a.ExpiresAt.Equal(b.ExpiresAt)
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
		return tokenResponse{}, transporthttp.RedactError(err)
	}
	defer response.Body.Close()
	var token tokenResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&token); err != nil {
		return token, fmt.Errorf("invalid token response")
	}
	if token.Error != "" {
		if values.Get("grant_type") == "refresh_token" && token.Error == "invalid_grant" {
			return token, fmt.Errorf("credentials expired or revoked; sign in again")
		}
		return token, fmt.Errorf("authorization server: %s", transporthttp.RedactDiagnostic(token.Error))
	}
	if token.ExpiresIn < 0 {
		return token, fmt.Errorf("invalid token expiry")
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
	return time.Now().Add(time.Duration(min(int64(seconds), int64(math.MaxInt64)/int64(time.Second))) * time.Second)
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
