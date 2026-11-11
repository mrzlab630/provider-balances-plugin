package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testJWT(exp int64) string {
	encode := func(value string) string { return base64.RawURLEncoding.EncodeToString([]byte(value)) }
	return encode(`{"alg":"none","typ":"JWT"}`) + "." + encode(`{"exp":`+strconv.FormatInt(exp, 10)+`}`) + ".signature"
}

func TestBearerAutoRefreshBeforeExpiryAndPersistsRotation(t *testing.T) {
	var refreshCalls, selfCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/auth/refresh":
			refreshCalls.Add(1)
			if r.Method != http.MethodPost || r.Header.Get("Origin") != "http://"+r.Host || r.Header.Get("Cookie") != "new_api_refresh=old-refresh" {
				t.Errorf("refresh request not formed correctly: method=%s origin=%q cookie=%q", r.Method, r.Header.Get("Origin"), r.Header.Get("Cookie"))
			}
			http.SetCookie(w, &http.Cookie{Name: refreshCookieName, Value: "rotated-refresh", Path: "/api/user/auth"})
			_, _ = w.Write([]byte(`{"success":true,"data":{"access_token":"new-token","token_type":"Bearer","access_expires_at":4102444800}}`))
		case "/api/user/self":
			selfCalls.Add(1)
			if r.Header.Get("Authorization") != "Bearer new-token" {
				t.Errorf("new token was not used: %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":1000000}}`))
		case "/api/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":500000,"quota_display_type":"USD"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	store, err := openStore(filepath.Join(t.TempDir(), "providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	p := provider{Name: "Provider", URL: upstream.URL, AuthMode: "bearer", Token: testJWT(1), AutoRefresh: true, RefreshCookie: "new_api_refresh=old-refresh"}
	view, err := store.save(p)
	if err != nil {
		t.Fatal(err)
	}
	result, status, err := store.refresh(context.Background(), view.ID)
	if err != nil || status != 200 || result.Balance == nil || result.Balance.Amount != 2 || result.Balance.Unit != "USD" {
		t.Fatalf("refresh failed: status=%d view=%+v err=%v", status, result, err)
	}
	if refreshCalls.Load() != 1 || selfCalls.Load() != 1 {
		t.Fatalf("unexpected calls: refresh=%d self=%d", refreshCalls.Load(), selfCalls.Load())
	}
	persisted := store.providers[0]
	if persisted.Token != "new-token" || persisted.RefreshCookie != "rotated-refresh" || persisted.TokenRefreshedAt == nil {
		t.Fatalf("rotated credentials not persisted: %+v", persisted)
	}
	listed := store.list()[0]
	if listed.HasRefreshCookie != true || listed.TokenExpiresAt != 4102444800 {
		t.Fatalf("safe token metadata missing: %+v", listed)
	}
	raw, _ := json.Marshal(listed)
	if strings.Contains(string(raw), "new-token") || strings.Contains(string(raw), "rotated-refresh") {
		t.Fatal("auto-refresh credentials leaked in provider view")
	}
	reloaded, err := openStore(store.path)
	if err != nil || reloaded.providers[0].Token != "new-token" || reloaded.providers[0].RefreshCookie != "rotated-refresh" {
		t.Fatalf("rotated pair lost after reload: %v", err)
	}
	p.ID, p.Token, p.RefreshCookie = view.ID, "", ""
	if view, err = store.save(p); err != nil || view.TokenExpiresAt != 4102444800 || !view.HasRefreshCookie {
		t.Fatalf("blank save lost credentials/expiry: %+v %v", view, err)
	}
	if _, _, err := store.refresh(context.Background(), view.ID); err != nil || refreshCalls.Load() != 1 {
		t.Fatal("valid opaque token was refreshed unnecessarily")
	}
}

func TestRefreshFailuresPreserveBalanceAndDoNotLoop(t *testing.T) {
	for _, tc := range []struct {
		name          string
		auto          bool
		refreshStatus int
		wantRefresh   int32
	}{
		{"disabled", false, 200, 0},
		{"session expired", true, 401, 1},
		{"new token rejected", true, 200, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var refreshCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/user/auth/refresh" {
					refreshCalls.Add(1)
					w.WriteHeader(tc.refreshStatus)
					_, _ = w.Write([]byte(`{"success":true,"data":{"access_token":"replacement"}}`))
					return
				}
				w.WriteHeader(401)
			}))
			defer upstream.Close()
			store, _ := openStore(filepath.Join(t.TempDir(), "providers.json"))
			view, err := store.save(provider{Name: "Provider", URL: upstream.URL, AuthMode: "bearer", Token: "old", AutoRefresh: tc.auto, RefreshCookie: "private-refresh"})
			if err != nil {
				t.Fatal(err)
			}
			store.providers[0].Balance = &balance{Amount: 2, Unit: "USD"}
			got, status, err := store.refresh(context.Background(), view.ID)
			if err != nil || status != 200 || got.LastError == "" || got.Balance == nil || got.Balance.Amount != 2 || refreshCalls.Load() != tc.wantRefresh {
				t.Fatalf("refresh failure mishandled: %+v status=%d calls=%d err=%v", got, status, refreshCalls.Load(), err)
			}
			if strings.Contains(got.LastError, "private-refresh") {
				t.Fatal("refresh error exposed credentials")
			}
		})
	}
}

func TestRotatedPairSavedBeforeFailedBalanceAndConcurrentEdits(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/user/auth/refresh" {
			close(started)
			<-release
			http.SetCookie(w, &http.Cookie{Name: refreshCookieName, Value: "rotated", Path: "/api/user/auth"})
			_, _ = w.Write([]byte(`{"success":true,"data":{"access_token":"updated","access_expires_at":4102444800}}`))
			return
		}
		w.WriteHeader(503)
	}))
	defer upstream.Close()
	store, _ := openStore(filepath.Join(t.TempDir(), "providers.json"))
	p := provider{Name: "Provider", URL: upstream.URL, AuthMode: "bearer", AutoRefresh: true, RefreshCookie: "initial"}
	view, err := store.save(p)
	if err != nil {
		t.Fatal(err)
	}
	p.ID = view.ID
	done := make(chan providerView, 1)
	go func() { got, _, _ := store.refresh(context.Background(), p.ID); done <- got }()
	<-started
	_, editErr := store.save(p)
	_, duplicateStatus, _ := store.refresh(context.Background(), p.ID)
	close(release)
	got := <-done
	if editErr == nil || duplicateStatus != 409 {
		t.Fatal("concurrent edit/refresh was accepted during token rotation")
	}
	if got.LastError == "" {
		t.Fatal("missing failed balance error")
	}
	reloaded, err := openStore(store.path)
	if err != nil || reloaded.providers[0].Token != "updated" || reloaded.providers[0].RefreshCookie != "rotated" {
		t.Fatalf("rotated pair lost after balance error: %v", err)
	}
	p.RefreshCookie = ""
	if _, err := store.save(p); err != nil {
		t.Fatal(err)
	}
	if store.providers[0].RefreshCookie != "rotated" {
		t.Fatal("blank edit reverted rotated cookie")
	}
	p.URL = "https://other.example"
	if _, err := store.save(p); err == nil {
		t.Fatal("refresh cookie reused on changed URL")
	}
	p.URL, p.AutoRefresh = upstream.URL, false
	if _, err := store.save(p); err != nil {
		t.Fatal(err)
	}
	if store.providers[0].RefreshCookie != "" {
		t.Fatal("disabling auto-refresh retained refresh secret")
	}
}

func TestBearerRefreshRejectsRedirects(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer origin.Close()
	if _, err := refreshBearer(context.Background(), provider{URL: origin.URL, RefreshCookie: "private"}); err == nil || reached.Load() {
		t.Fatal("refresh followed redirect")
	}
}

func TestBearerExpiryHints(t *testing.T) {
	if bearerExpiry("opaque") != 0 || bearerExpiry("a.b.c") != 0 || bearerExpiry(testJWT(-1)) != 0 {
		t.Fatal("malformed expiry accepted")
	}
	if !needsBearerRefresh(provider{}) || !needsBearerRefresh(provider{Token: "token", TokenExpiresAt: time.Now().Add(30 * time.Second).Unix()}) || needsBearerRefresh(provider{Token: "token", TokenExpiresAt: time.Now().Add(time.Hour).Unix()}) {
		t.Fatal("expiry scheduling incorrect")
	}
}

func TestBearerAutoRefreshAfterAuthorizationFailure(t *testing.T) {
	var selfCalls, refreshCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/auth/refresh":
			refreshCalls.Add(1)
			_, _ = w.Write([]byte(`{"success":true,"data":{"access_token":"retry-token","token_type":"Bearer"}}`))
		case "/api/user/self":
			selfCalls.Add(1)
			if selfCalls.Load() == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"success":false,"code":"AUTH_TOKEN_EXPIRED"}`))
				return
			}
			if r.Header.Get("Authorization") != "Bearer retry-token" {
				t.Errorf("retry token was not used")
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":500000}}`))
		case "/api/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":500000,"quota_display_type":"USD"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	store, err := openStore(filepath.Join(t.TempDir(), "providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	view, err := store.save(provider{Name: "Provider", URL: upstream.URL, AuthMode: "bearer", Token: "live-token", AutoRefresh: true, RefreshCookie: "refresh-token"})
	if err != nil {
		t.Fatal(err)
	}
	result, status, err := store.refresh(context.Background(), view.ID)
	if err != nil || status != 200 || result.Balance == nil || result.Balance.Amount != 1 {
		t.Fatalf("retry refresh failed: status=%d view=%+v err=%v", status, result, err)
	}
	if selfCalls.Load() != 2 || refreshCalls.Load() != 1 {
		t.Fatalf("unexpected retry calls: self=%d refresh=%d", selfCalls.Load(), refreshCalls.Load())
	}
}

func TestRefreshCookieValidation(t *testing.T) {
	for _, raw := range []string{"", "other=value", "new_api_refresh=", "new_api_refresh=abc\ndef", "token with spaces"} {
		if _, err := normalizeRefreshCookie(raw); err == nil {
			t.Fatalf("invalid refresh cookie accepted: %q", raw)
		}
	}
	for _, raw := range []string{"token", "new_api_refresh=token", "Cookie: new_api_refresh=token; new_api_has_session=1"} {
		got, err := normalizeRefreshCookie(raw)
		if err != nil || got != "token" {
			t.Fatalf("cookie normalization: raw=%q got=%q err=%v", raw, got, err)
		}
	}
	value, header, err := normalizeRefreshCookieInput("Cookie: cf_clearance=clear; bnc-uuid=device; new_api_refresh=token; new_api_has_session=1")
	if err != nil || value != "token" || header != "cf_clearance=clear; bnc-uuid=device; new_api_refresh=token; new_api_has_session=1" {
		t.Fatalf("complete Cookie header was not preserved: value=%q header=%q err=%v", value, header, err)
	}
	if _, _, err := normalizeRefreshCookieInput("cf_clearance=clear; new_api_has_session=1"); err == nil || !strings.Contains(err.Error(), "new_api_has_session") {
		t.Fatalf("session hint should explain missing refresh credential: %v", err)
	}
}

func TestBearerRefreshSendsAuxiliaryCookiesAndRotatesHeader(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/auth/refresh" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Cookie"); got != "cf_clearance=clear; new_api_refresh=initial" {
			t.Errorf("auxiliary cookies were not sent: %q", got)
		}
		http.SetCookie(w, &http.Cookie{Name: refreshCookieName, Value: "rotated", Path: "/api/user/auth"})
		_, _ = w.Write([]byte(`{"success":true,"data":{"access_token":"token","access_expires_at":4102444800}}`))
	}))
	defer upstream.Close()
	p, err := refreshBearer(context.Background(), provider{URL: upstream.URL, RefreshCookie: "initial", RefreshCookieHeader: "cf_clearance=clear; new_api_refresh=initial"})
	if err != nil {
		t.Fatal(err)
	}
	if p.RefreshCookie != "rotated" || p.RefreshCookieHeader != "cf_clearance=clear; new_api_refresh=rotated" {
		t.Fatalf("rotated header not retained: %+v", p)
	}
}
