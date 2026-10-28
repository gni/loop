package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"loop/pkg/config"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCredentialRefreshAcrossStores(t *testing.T) {
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("refresh_token") != "refresh" {
			t.Error("missing refresh credential")
		}
		refreshes.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-token", "expires_in": 3600})
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "auth.json")
	first, second := NewStore(path), NewStore(path)
	profile := config.ProviderConfig{Endpoint: server.URL, OAuth: &config.OAuthConfig{TokenURL: server.URL, ClientID: "client"}}
	if err := first.Save("test", Credential{Type: "oauth", Issuer: server.URL, RefreshToken: "refresh", AccessToken: "old", ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for _, store := range []*Store{first, second} {
		wait.Add(1)
		go func(s *Store) {
			defer wait.Done()
			key, err := s.Resolve(context.Background(), "test", profile)
			if err != nil || key != "new-token" {
				t.Errorf("resolve: %q %v", key, err)
			}
		}(store)
	}
	wait.Wait()
	if refreshes.Load() != 1 {
		t.Fatalf("refresh count %d", refreshes.Load())
	}
	if _, err := first.Resolve(context.Background(), "test", config.ProviderConfig{Endpoint: "https://elsewhere.invalid"}); err == nil {
		t.Fatal("credentials crossed endpoint boundary")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("credential permissions %o", info.Mode().Perm())
	}
}

func TestPKCELoginValidatesStateAndVerifier(t *testing.T) {
	var challenge string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			t.Error("PKCE verifier does not match challenge")
		}
		if r.Form.Get("code") != "accepted" {
			t.Error("unexpected authorization code")
		}
		fmt.Fprint(w, `{"access_token":"access","refresh_token":"refresh","expires_in":3600}`)
	}))
	defer server.Close()
	store := NewStore(filepath.Join(t.TempDir(), "auth.json"))
	profile := config.ProviderConfig{Endpoint: server.URL, OAuth: &config.OAuthConfig{AuthorizationURL: server.URL + "/authorize", TokenURL: server.URL + "/token", ClientID: "client"}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := store.Login(ctx, "test", profile, false, func(link string) {
		authorization, err := url.Parse(link)
		if err != nil {
			t.Error(err)
			return
		}
		challenge = authorization.Query().Get("code_challenge")
		callback := authorization.Query().Get("redirect_uri")
		response, err := http.Get(callback + "?state=wrong&code=invalid")
		if err != nil {
			t.Error(err)
			return
		}
		response.Body.Close()
		if response.StatusCode != 400 {
			t.Error("invalid state accepted")
		}
		response, err = http.Get(callback + "?state=" + url.QueryEscape(authorization.Query().Get("state")) + "&code=accepted")
		if err != nil {
			t.Error(err)
			return
		}
		response.Body.Close()
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.Resolve(ctx, "test", profile)
	if err != nil || key != "access" {
		t.Fatalf("resolve: %q %v", key, err)
	}
}

func TestEnvironmentReferencesStayReferences(t *testing.T) {
	t.Setenv("LOOP_AUTH_TEST", "secret")
	path := filepath.Join(t.TempDir(), "auth.json")
	store := NewStore(path)
	if err := store.Save("test", Credential{Type: "api_key", Environment: "LOOP_AUTH_TEST"}); err != nil {
		t.Fatal(err)
	}
	if key, err := store.Resolve(context.Background(), "test", config.ProviderConfig{}); err != nil || key != "secret" {
		t.Fatalf("resolve: %q %v", key, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]Credential
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["test"].Key != "" {
		t.Fatal("resolved environment secret was persisted")
	}
}
