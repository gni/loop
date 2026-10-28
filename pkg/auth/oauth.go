package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"loop/pkg/config"
)

func (s *Store) Login(ctx context.Context, name string, profile config.ProviderConfig, device bool, notify func(string)) error {
	if profile.OAuth == nil {
		return fmt.Errorf("provider does not configure OAuth; configure an API key or OAuth endpoints")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if device {
		return s.deviceLogin(ctx, name, profile, notify)
	}
	return s.browserLogin(ctx, name, profile, notify)
}

func (s *Store) browserLogin(ctx context.Context, name string, profile config.ProviderConfig, notify func(string)) error {
	oauth := *profile.OAuth
	if err := secureURL(oauth.AuthorizationURL); err != nil {
		return err
	}
	if err := secureURL(oauth.TokenURL); err != nil {
		return err
	}
	listenAddress := "127.0.0.1:0"
	callbackPath := "/callback"
	if oauth.RedirectURI != "" {
		callback, err := url.Parse(oauth.RedirectURI)
		if err != nil || callback.Scheme != "http" || (callback.Hostname() != "localhost" && callback.Hostname() != "127.0.0.1" && callback.Hostname() != "::1") {
			return fmt.Errorf("OAuth callback must be a loopback HTTP URL")
		}
		listenAddress = callback.Host
		callbackPath = callback.Path
		if callback.Port() == "" {
			listenAddress = net.JoinHostPort(callback.Hostname(), "0")
		}
	}
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return err
	}
	defer listener.Close()
	redirectURI := "http://" + listener.Addr().String() + callbackPath
	if oauth.RedirectURI != "" {
		u, _ := url.Parse(oauth.RedirectURI)
		if u.Port() != "" {
			redirectURI = oauth.RedirectURI
		}
	}
	if oauth.ClientID == "" && oauth.RegistrationURL != "" {
		oauth.ClientID, err = s.register(ctx, oauth.RegistrationURL, redirectURI)
		if err != nil {
			return err
		}
	}
	if oauth.ClientID == "" {
		return fmt.Errorf("OAuth client_id is required")
	}
	var random [64]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	state := base64.RawURLEncoding.EncodeToString(random[:32])
	verifier := base64.RawURLEncoding.EncodeToString(random[32:])
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	endpoint, err := url.Parse(oauth.AuthorizationURL)
	if err != nil {
		return err
	}
	query := endpoint.Query()
	for key, value := range map[string]string{"response_type": "code", "client_id": oauth.ClientID, "redirect_uri": redirectURI, "scope": strings.Join(oauth.Scopes, " "), "state": state, "code_challenge": challenge, "code_challenge_method": "S256"} {
		query.Set(key, value)
	}
	endpoint.RawQuery = query.Encode()
	codes := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(state)) != 1 {
			http.Error(w, "Invalid authorization state", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Authorization was not completed", http.StatusBadRequest)
			return
		}
		select {
		case codes <- code:
			fmt.Fprint(w, "Authentication complete. You can close this window.")
		default:
			http.Error(w, "Callback already handled", http.StatusConflict)
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer server.Close()
	notify(endpoint.String())
	var code string
	select {
	case code = <-codes:
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
	token, err := s.token(ctx, oauth.TokenURL, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {oauth.ClientID}, "client_secret": {oauth.ClientSecret}, "redirect_uri": {redirectURI}, "code_verifier": {verifier}})
	if err != nil {
		return err
	}
	return s.Save(name, Credential{Type: "oauth", Issuer: profile.Endpoint, AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, ExpiresAt: tokenExpiry(token.ExpiresIn), ClientID: oauth.ClientID})
}

func (s *Store) deviceLogin(ctx context.Context, name string, profile config.ProviderConfig, notify func(string)) error {
	oauth := profile.OAuth
	if err := secureURL(oauth.DeviceURL); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", oauth.DeviceURL, strings.NewReader(url.Values{"client_id": {oauth.ClientID}, "scope": {strings.Join(oauth.Scopes, " ")}}.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.Client.Do(request)
	if err != nil {
		return err
	}
	var device struct {
		DeviceCode           string `json:"device_code"`
		UserCode             string `json:"user_code"`
		VerificationURI      string `json:"verification_uri"`
		VerificationComplete string `json:"verification_uri_complete"`
		Interval             int    `json:"interval"`
		ExpiresIn            int    `json:"expires_in"`
	}
	err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&device)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || device.DeviceCode == "" {
		return fmt.Errorf("invalid device authorization response")
	}
	verify := device.VerificationComplete
	if verify == "" {
		verify = device.VerificationURI
	}
	notify(verify + "\nCode: " + device.UserCode)
	if device.ExpiresIn > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(device.ExpiresIn)*time.Second)
		defer cancel()
	}
	interval := time.Duration(max(1, device.Interval)) * time.Second
	for {
		timer := time.NewTimer(interval)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
		token, err := s.token(ctx, oauth.TokenURL, url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {device.DeviceCode}, "client_id": {oauth.ClientID}})
		if token.Error == "authorization_pending" {
			continue
		}
		if token.Error == "slow_down" {
			interval += 5 * time.Second
			continue
		}
		if err != nil {
			return err
		}
		return s.Save(name, Credential{Type: "oauth", Issuer: profile.Endpoint, AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, ExpiresAt: tokenExpiry(token.ExpiresIn), ClientID: oauth.ClientID})
	}
}

func (s *Store) register(ctx context.Context, endpoint, redirect string) (string, error) {
	if err := secureURL(endpoint); err != nil {
		return "", err
	}
	data, err := json.Marshal(map[string]any{"client_name": "loop", "redirect_uris": []string{redirect}, "token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(string(data)))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.Client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var registration struct {
		ClientID string `json:"client_id"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&registration); err != nil {
		return "", fmt.Errorf("invalid client registration response")
	}
	if response.StatusCode >= 300 || registration.ClientID == "" {
		return "", fmt.Errorf("OAuth client registration failed (HTTP %d)", response.StatusCode)
	}
	return registration.ClientID, nil
}
