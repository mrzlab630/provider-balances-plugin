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

func TestAnyModelUsesBothBalancesWithoutRecalculation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		amount     float64
		tokens     int64
	}{
		{"different units", `{"balance":123456789,"balanceUsd":7.1256789,"debt":500,"user":{"tokenBalance":999,"email":"private@example.test"},"keys":[{"key":"private-api-key"}]}`, 7.1256789, 123456789},
		{"zero", `{"balance":0,"balanceUsd":0}`, 0, 0},
		{"negative", `{"balance":-100,"balanceUsd":-0.005}`, -0.005, -100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/api/me" || r.Header.Get("Cookie") != "am_session=session; cf_clearance=challenge" {
					t.Error("unexpected AnyModel request")
				}
				if r.Header.Get("Authorization") != "" || r.Header.Get("New-Api-User") != "" || r.Header.Get("Accept") != "application/json" {
					t.Error("unexpected AnyModel headers")
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer upstream.Close()
			got, err := fetchBalance(context.Background(), provider{URL: upstream.URL, APIType: apiTypeAnyModel, AuthMode: "cookie", Token: "am_session=session; cf_clearance=challenge"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Amount != tc.amount || got.Unit != "USD" || got.Tokens == nil || *got.Tokens != tc.tokens || got.CheckedAt.IsZero() || calls.Load() != 1 {
				t.Fatalf("unexpected balance: %+v; requests: %d", got, calls.Load())
			}
			raw, _ := json.Marshal(got)
			for _, private := range []string{"private@example.test", "private-api-key", "cf_clearance", "am_session", "keys", "user"} {
				if strings.Contains(string(raw), private) {
					t.Fatalf("balance contains private field: %s", private)
				}
			}
		})
	}
}

func TestAnyModelRejectsInvalidResponsesWithoutExposingBody(t *testing.T) {
	for _, body := range []string{
		`<html>private-api-key challenge</html>`, `{}`, `null`,
		`{"balance":1}`, `{"balanceUsd":1}`, `{"balance":null,"balanceUsd":1}`,
		`{"balance":1,"balanceUsd":null}`, `{"balance":"1","balanceUsd":1}`,
		`{"balance":1,"balanceUsd":"1"}`, `{"balance":1.5,"balanceUsd":1}`,
		`{"balance":9223372036854775808,"balanceUsd":1}`,
		`{"balance":1,"balanceUsd":1e999}`, strings.Repeat("x", (1<<20)+1),
	} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		_, err := fetchBalance(context.Background(), provider{URL: upstream.URL, APIType: apiTypeAnyModel, AuthMode: "cookie", Token: "am_session=session"})
		upstream.Close()
		if err == nil || strings.Contains(err.Error(), "private-api-key") {
			t.Fatalf("invalid AnyModel response not safely rejected: %v", err)
		}
	}
}

func TestAnyModelRedirectNeverForwardsCookie(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer origin.Close()
	_, err := fetchBalance(context.Background(), provider{URL: origin.URL, APIType: apiTypeAnyModel, AuthMode: "cookie", Token: "am_session=session"})
	if err == nil || reached.Load() {
		t.Fatal("AnyModel redirect followed")
	}
}

func TestAnyModelManagementPersistenceAndFailures(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(`{"balance":123456789,"balanceUsd":7.1256789,"keys":[{"key":"private-api-key"}]}`))
	}))
	defer upstream.Close()
	path := filepath.Join(t.TempDir(), "providers.json")
	store, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	p := provider{Name: "AnyModel", URL: upstream.URL, APIType: apiTypeAnyModel, AuthMode: "cookie", Token: "Cookie: am_session=private-session; cf_clearance=private-challenge"}
	raw, _ := json.Marshal(p)
	saved := handleManagement(store, managementRequest{Method: "PUT", Path: apiPath + "/providers", Body: raw})
	if saved.StatusCode != 200 {
		t.Fatalf("save: %s", saved.Body)
	}
	view := store.list()[0]
	if view.APIType != apiTypeAnyModel || !view.HasToken {
		t.Fatalf("AnyModel settings not saved: %+v", view)
	}
	got, code, err := store.refresh(context.Background(), view.ID)
	if err != nil || code != 200 || got.Balance == nil || got.Balance.Tokens == nil || *got.Balance.Tokens != 123456789 {
		t.Fatalf("AnyModel refresh failed: %+v %d %v", got, code, err)
	}
	p.ID, p.Token, p.Currency = view.ID, "", "EUR"
	if _, err := store.save(p); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []int{401, 403, 429, 500} {
		status.Store(int32(failure))
		got, code, err = store.refresh(context.Background(), view.ID)
		if err != nil || code != 200 || got.LastError == "" || got.Balance == nil || got.Balance.Amount != 7.1256789 || got.Balance.Tokens == nil || *got.Balance.Tokens != 123456789 || got.Balance.DisplayUnit != "EUR" {
			t.Fatalf("failed request lost cached balance: %+v %d %v", got, code, err)
		}
	}
	reloaded, err := openStore(path)
	if err != nil || reloaded.list()[0].Balance.Tokens == nil || *reloaded.list()[0].Balance.Tokens != 123456789 || reloaded.list()[0].APIType != apiTypeAnyModel {
		t.Fatalf("AnyModel balance not reloaded: %v", err)
	}
	listed := handleManagement(store, managementRequest{Method: "GET", Path: apiPath + "/providers"})
	for _, private := range []string{"private-session", "private-challenge", "private-api-key"} {
		if strings.Contains(string(listed.Body), private) || strings.Contains(string(saved.Body), private) {
			t.Fatalf("management response exposed: %s", private)
		}
	}
	persisted, _ := os.ReadFile(path)
	if strings.Contains(string(persisted), "private-api-key") {
		t.Fatal("AnyModel API keys persisted")
	}
}

func TestProviderTypeValidationAndLegacyRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(path, []byte(`[{"id":"legacy","name":"Legacy","url":"https://example.test","auth_mode":"cookie","token":"am_session=legacy"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := openStore(path)
	if err != nil || store.list()[0].APIType != apiTypeNewAPI {
		t.Fatalf("legacy provider not preserved: %v", err)
	}
	p := provider{ID: "legacy", Name: "Legacy", URL: "https://example.test", AuthMode: "cookie"}
	if _, err := store.save(p); err != nil {
		t.Fatalf("legacy blank-token save failed: %v", err)
	}
	p.APIType = apiTypeAnyModel
	if _, err := store.save(p); err == nil {
		t.Fatal("changing API type reused saved credentials")
	}
	p.Token = "am_session=new-session"
	if _, err := store.save(p); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []provider{
		{APIType: "unknown", AuthMode: "cookie", Token: "am_session=session"},
		{APIType: apiTypeAnyModel, AuthMode: "bearer", Token: "session"},
		{APIType: apiTypeAnyModel, AuthMode: "cookie", Token: "am_auth=1"},
		{APIType: apiTypeAnyModel, AuthMode: "cookie", Token: "am_session="},
		{APIType: apiTypeAnyModel, AuthMode: "cookie", Token: "am_session=session", UserID: "42"},
		{APIType: apiTypeAnyModel, AuthMode: "cookie", Token: "am_session=session", AutoRefresh: true},
		{APIType: apiTypeAnyModel, AuthMode: "cookie", Token: "am_session=session", RefreshCookie: "new_api_refresh=secret"},
	} {
		invalid.Name, invalid.URL = "Invalid", "https://example.test"
		if _, err := store.save(invalid); err == nil {
			t.Fatal("invalid AnyModel settings accepted")
		}
	}
}
