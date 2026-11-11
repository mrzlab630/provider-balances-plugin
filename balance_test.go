package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProviderLifecycle(t *testing.T) {
	var fail, statusFails atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/self":
			if r.Header.Get("Authorization") != "Bearer secret-token" || r.Header.Get("New-Api-User") != "42" {
				t.Error("provider credentials were not forwarded correctly")
			}
			if fail.Load() {
				http.Error(w, "expired secret-token", 401)
				return
			}
			_, _ = w.Write([]byte("{\"success\":true,\"data\":{\"quota\":1250000}}"))
		case "/api/status":
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Error("credentials sent to public status endpoint")
			}
			if statusFails.Load() {
				http.Error(w, "unavailable", 503)
				return
			}
			_, _ = w.Write([]byte("{\"success\":true,\"data\":{\"quota_per_unit\":500000,\"quota_display_type\":\"CNY\",\"usd_exchange_rate\":7}}"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	path := filepath.Join(t.TempDir(), "private", "providers.json")
	store, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	request := provider{Name: "Provider", URL: upstream.URL + "/v1/", AuthMode: "bearer", Token: "Bearer secret-token", UserID: "42"}
	raw, _ := json.Marshal(request)
	saved := handleManagement(store, managementRequest{Method: "PUT", Path: apiPath + "/providers", Body: raw})
	if saved.StatusCode != 200 {
		t.Fatalf("save: %s", saved.Body)
	}
	if strings.Contains(string(saved.Body), "secret-token") {
		t.Fatal("save leaked token")
	}
	views := store.list()
	if len(views) != 1 || views[0].URL != upstream.URL || !views[0].HasToken {
		t.Fatalf("unexpected provider: %+v", views)
	}
	id := views[0].ID
	result, status, err := store.refresh(context.Background(), id)
	if err != nil || status != 200 {
		t.Fatalf("refresh: %d %v", status, err)
	}
	if result.Balance == nil || result.Balance.Amount != 17.5 || result.Balance.Unit != "CNY" {
		t.Fatalf("wrong balance: %+v", result.Balance)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credentials file permissions: %v %v", info, err)
	}
	reloaded, err := openStore(path)
	if err != nil || reloaded.list()[0].Balance.Amount != 17.5 {
		t.Fatalf("reload failed: %v", err)
	}
	request.ID, request.Name, request.Token = id, "Renamed", ""
	if _, err := store.save(request); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	result, _, err = store.refresh(context.Background(), id)
	if err != nil || result.Balance.Amount != 17.5 || result.LastError == "" {
		t.Fatalf("last balance lost: %+v %v", result, err)
	}
	listed := handleManagement(store, managementRequest{Method: "GET", Path: apiPath + "/providers"})
	if strings.Contains(string(listed.Body), "secret-token") || strings.Contains(string(listed.Body), "\"token\"") {
		t.Fatalf("list leaked credentials: %s", listed.Body)
	}
	fail.Store(false)
	statusFails.Store(true)
	result, _, err = store.refresh(context.Background(), id)
	if err != nil || result.Balance.Unit != "quota" || result.Balance.Amount != 1250000 || result.Balance.Warning == "" {
		t.Fatalf("missing currency: %+v %v", result, err)
	}
	if err := store.remove(id); err != nil || len(store.list()) != 0 {
		t.Fatalf("delete failed: %v", err)
	}
}

func TestNewAPICurrencyAndCookie(t *testing.T) {
	for _, tc := range []struct {
		name, settings, unit string
		quota, amount        float64
	}{
		{"zero", "{\"quota_per_unit\":500000,\"quota_display_type\":\"USD\"}", "USD", 0, 0},
		{"tokens", "{\"quota_display_type\":\"TOKENS\"}", "tokens", 12345, 12345},
		{"custom", "{\"quota_per_unit\":500000,\"quota_display_type\":\"CUSTOM\",\"custom_currency_symbol\":\"€\",\"custom_currency_exchange_rate\":0.9}", "€", 5000000, 9},
		{"negative", "{\"quota_per_unit\":500000,\"quota_display_type\":\"USD\"}", "USD", -500000, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var data any = json.RawMessage(tc.settings)
				if r.URL.Path == "/api/user/self" {
					if r.Header.Get("Cookie") != "session=secret" || r.Header.Get("Authorization") != "" {
						t.Error("cookie authentication failed")
					}
					data = map[string]float64{"quota": tc.quota}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
			}))
			defer upstream.Close()
			got, err := fetchBalance(context.Background(), provider{URL: upstream.URL, AuthMode: "cookie", Token: "session=secret"})
			if err != nil || got.Unit != tc.unit || got.Amount != tc.amount {
				t.Fatalf("balance: %+v %v", got, err)
			}
		})
	}
}

func TestRejectInvalidResponsesAndRedirects(t *testing.T) {
	for _, body := range []string{"<html>challenge</html>", "{\"success\":true,\"data\":{}}", "{\"success\":false,\"message\":\"secret\"}"} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		_, err := fetchBalance(context.Background(), provider{URL: upstream.URL, AuthMode: "bearer", Token: "secret"})
		upstream.Close()
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid response not safely rejected: %v", err)
		}
	}
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Store(true) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer origin.Close()
	if _, err := fetchBalance(context.Background(), provider{URL: origin.URL, Token: "secret", AuthMode: "cookie"}); err == nil || redirected.Load() {
		t.Fatal("redirect forwarded credentials")
	}
}

func TestCredentialReplacementAndConcurrentEdit(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/user/self" {
			close(started)
			<-release
		}
		_, _ = w.Write([]byte("{\"success\":true,\"data\":{\"quota\":1}}"))
	}))
	defer upstream.Close()
	store, err := openStore(filepath.Join(t.TempDir(), "providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	p := provider{Name: "Provider", URL: upstream.URL, AuthMode: "bearer", Token: "old"}
	view, err := store.save(p)
	if err != nil {
		t.Fatal(err)
	}
	p.ID = view.ID
	done := make(chan int)
	go func() { _, status, _ := store.refresh(context.Background(), p.ID); done <- status }()
	<-started
	p.Token = "replacement"
	if _, err := store.save(p); err != nil {
		t.Fatal(err)
	}
	close(release)
	if status := <-done; status != 409 {
		t.Fatalf("stale response accepted: %d", status)
	}
	if store.list()[0].Balance != nil {
		t.Fatal("old credentials balance retained")
	}
	p.URL, p.Token = "https://other.example", ""
	if _, err := store.save(p); err == nil {
		t.Fatal("changed URL reused credentials")
	}
	p.URL, p.Token = upstream.URL, "header\r\ninjection"
	if _, err := store.save(p); err == nil {
		t.Fatal("header injection accepted")
	}
}
