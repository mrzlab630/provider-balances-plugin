package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCurrencySelectionPersistsWithoutChangingBalance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	store, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	p := provider{Name: "Provider", URL: "https://example.com", AuthMode: "bearer", Token: "secret"}
	view, err := store.save(p)
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Token = view.ID, ""
	checked := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	store.providers[0].Balance = &balance{Quota: 1250000, Amount: 17.5, Unit: "CNY", CheckedAt: checked}
	store.providers[0].LastError = "previous refresh failed"
	for _, tc := range []struct{ currency, custom, display string }{
		{"RUB", "", "RUB"}, {"BTC", "", "BTC"},
		{"CUSTOM", "  💎 баллы  ", "💎 баллы"},
		{"CUSTOM", "<b>credit</b>", "<b>credit</b>"}, {"", "", ""},
	} {
		p.Currency, p.CustomCurrency = tc.currency, tc.custom
		raw, _ := json.Marshal(p)
		response := handleManagement(store, managementRequest{Method: "PUT", Path: apiPath + "/providers", Body: raw})
		if response.StatusCode != 200 {
			t.Fatalf("save %s: %s", tc.currency, response.Body)
		}
		var data struct {
			Provider providerView `json:"provider"`
		}
		if err := json.Unmarshal(response.Body, &data); err != nil {
			t.Fatal(err)
		}
		view = data.Provider
		if view.Currency != tc.currency || view.Balance == nil || view.Balance.DisplayUnit != tc.display {
			t.Fatalf("wrong currency: %+v", view)
		}
		if view.Balance.Amount != 17.5 || view.Balance.Unit != "CNY" || !view.Balance.CheckedAt.Equal(checked) || view.LastError == "" {
			t.Fatalf("currency selection changed cached balance: %+v", view)
		}
		if strings.Contains(string(response.Body), "secret") {
			t.Fatal("currency save leaked credentials")
		}
		reloaded, err := openStore(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := reloaded.list()[0]; got.Currency != tc.currency || got.Balance.DisplayUnit != tc.display {
			t.Fatalf("currency lost on reload: %+v", got)
		}
		if reloaded.providers[0].Balance.DisplayUnit != "" {
			t.Fatal("display override was stored inside the source balance")
		}
	}
}

func TestCurrencyValidationAndIndependentProviders(t *testing.T) {
	store, err := openStore(filepath.Join(t.TempDir(), "providers.json"))
	if err != nil {
		t.Fatal(err)
	}
	p := provider{Name: "Provider", URL: "https://example.com", AuthMode: "bearer", Token: "secret"}
	for _, tc := range []struct{ currency, custom string }{
		{"FAKE", ""}, {"CUSTOM", ""}, {"CUSTOM", "   "},
		{"CUSTOM", strings.Repeat("₽", 25)}, {"CUSTOM", "a\nb"}, {"CUSTOM", "a\tb"},
	} {
		p.Currency, p.CustomCurrency = tc.currency, tc.custom
		if _, err := store.save(p); err == nil {
			t.Fatalf("invalid currency accepted: %+v", tc)
		}
	}
	for _, code := range []string{"rub", "USDT", "AUTO"} {
		p.Currency, p.CustomCurrency = code, "ignored"
		if _, err := store.save(p); err != nil {
			t.Fatal(err)
		}
	}
	views := store.list()
	if len(views) != 3 || views[0].Currency != "RUB" || views[1].Currency != "USDT" || views[2].Currency != "" {
		t.Fatalf("provider currencies are not independent: %+v", views)
	}
	if views[0].CustomCurrency != "" {
		t.Fatal("unused custom currency was retained")
	}
	for _, unit := range []string{"quota", "tokens"} {
		store.providers[0].Balance = &balance{Quota: 1250000, Amount: 1250000, Unit: unit}
		if got := store.list()[0].Balance; got.DisplayUnit != "" || got.Unit != unit {
			t.Fatalf("raw units mislabeled as money: %+v", got)
		}
	}
}

func TestMissingCurrencyDoesNotAssumeUSD(t *testing.T) {
	for _, settings := range []string{
		`{"quota_per_unit":500000}`,
		`{"quota_per_unit":500000,"quota_display_type":"UNKNOWN"}`,
		`{"quota_per_unit":500000,"quota_display_type":"CNY"}`,
		`{"quota_per_unit":500000,"quota_display_type":"CUSTOM"}`,
	} {
		t.Run(settings, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data := json.RawMessage(settings)
				if r.URL.Path == "/api/user/self" {
					data = json.RawMessage(`{"quota":1250000}`)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
			}))
			defer upstream.Close()
			got, err := fetchBalance(context.Background(), provider{URL: upstream.URL, AuthMode: "bearer", Token: "secret"})
			if err != nil || got.Unit != "units" || got.Amount != 2.5 || got.Warning == "" || strings.Contains(got.Warning, "USD") {
				t.Fatalf("unexpected currency fallback: %+v %v", got, err)
			}
		})
	}
}
