package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

type provider struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	URL            string `json:"url"`
	AuthMode       string `json:"auth_mode"`
	Token          string `json:"token"`
	UserID         string `json:"user_id"`
	Currency       string `json:"currency"`
	CustomCurrency string `json:"custom_currency,omitempty"`
	AutoRefresh    bool   `json:"auto_refresh"`
	RefreshCookie  string `json:"refresh_cookie,omitempty"`
	// RefreshCookieHeader keeps the complete Cookie header when the provider
	// needs auxiliary browser cookies (for example, a Cloudflare clearance)
	// alongside new_api_refresh. It is never returned in providerView.
	RefreshCookieHeader string     `json:"refresh_cookie_header,omitempty"`
	TokenExpiresAt      int64      `json:"token_expires_at,omitempty"`
	TokenRefreshedAt    *time.Time `json:"token_refreshed_at,omitempty"`
	Balance             *balance   `json:"balance,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
	AttemptedAt         *time.Time `json:"attempted_at,omitempty"`
}

type providerView struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	URL              string     `json:"url"`
	AuthMode         string     `json:"auth_mode"`
	HasToken         bool       `json:"has_token"`
	UserID           string     `json:"user_id"`
	Currency         string     `json:"currency"`
	CustomCurrency   string     `json:"custom_currency,omitempty"`
	AutoRefresh      bool       `json:"auto_refresh"`
	HasRefreshCookie bool       `json:"has_refresh_cookie"`
	TokenExpiresAt   int64      `json:"token_expires_at,omitempty"`
	TokenRefreshedAt *time.Time `json:"token_refreshed_at,omitempty"`
	Balance          *balance   `json:"balance,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
	AttemptedAt      *time.Time `json:"attempted_at,omitempty"`
	Refreshing       bool       `json:"refreshing"`
}

type providerStore struct {
	mu        sync.Mutex
	path      string
	providers []provider
	busy      map[string]bool
	renewing  map[string]bool
	revisions map[string]uint64
}

func openStore(path string) (*providerStore, error) {
	store := &providerStore{path: path, providers: []provider{}, busy: map[string]bool{}, renewing: map[string]bool{}, revisions: map[string]uint64{}}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return store, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return nil, errors.New("cannot read provider balance data file")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.New("cannot secure provider balance data file")
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &store.providers) != nil {
		return nil, errors.New("invalid provider balance data file")
	}
	if store.providers == nil {
		store.providers = []provider{}
	}
	for i := range store.providers {
		if store.providers[i].AuthMode == "bearer" && store.providers[i].TokenExpiresAt == 0 {
			store.providers[i].TokenExpiresAt = bearerExpiry(store.providers[i].Token)
		}
	}
	return store, nil
}

func (store *providerStore) persist(providers []provider) error {
	raw, err := json.MarshalIndent(providers, "", "  ")
	if err != nil {
		return errors.New("Не удалось сохранить настройки")
	}
	dir := filepath.Dir(store.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.New("Не удалось создать каталог настроек")
	}
	f, err := os.CreateTemp(dir, ".provider-balances-*")
	if err != nil {
		return errors.New("Не удалось создать файл настроек")
	}
	defer os.Remove(f.Name())
	_, errWrite := f.Write(raw)
	errSync := f.Sync()
	errClose := f.Close()
	if errWrite != nil || errSync != nil || errClose != nil || os.Rename(f.Name(), store.path) != nil {
		return errors.New("Не удалось сохранить настройки на диск")
	}
	store.providers = providers
	return nil
}

func (store *providerStore) view(p provider) providerView {
	display := p.Balance
	if display != nil && display.Unit != "quota" && display.Unit != "tokens" && p.Currency != "" {
		copy := *display
		copy.DisplayUnit = p.Currency
		if p.Currency == "CUSTOM" {
			copy.DisplayUnit = p.CustomCurrency
		}
		display = &copy
	}
	return providerView{ID: p.ID, Name: p.Name, URL: p.URL, AuthMode: p.AuthMode, HasToken: p.Token != "", UserID: p.UserID, Currency: p.Currency, CustomCurrency: p.CustomCurrency, AutoRefresh: p.AutoRefresh, HasRefreshCookie: p.RefreshCookie != "", TokenExpiresAt: p.TokenExpiresAt, TokenRefreshedAt: p.TokenRefreshedAt, Balance: display, LastError: p.LastError, AttemptedAt: p.AttemptedAt, Refreshing: store.busy[p.ID]}
}

func (store *providerStore) list() []providerView {
	store.mu.Lock()
	defer store.mu.Unlock()
	views := make([]providerView, 0, len(store.providers))
	for _, p := range store.providers {
		views = append(views, store.view(p))
	}
	return views
}

func (store *providerStore) save(p provider) (providerView, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.renewing[p.ID] {
		return providerView{}, errors.New("Токен сейчас обновляется. Дождитесь завершения и повторите сохранение")
	}
	p.Name, p.Token, p.UserID = strings.TrimSpace(p.Name), strings.TrimSpace(p.Token), strings.TrimSpace(p.UserID)
	if p.AuthMode != "bearer" && p.AutoRefresh {
		return providerView{}, errors.New("Автообновление доступно для Bearer-токена")
	}
	if p.RefreshCookie != "" {
		var err error
		p.RefreshCookie, p.RefreshCookieHeader, err = normalizeRefreshCookieInput(p.RefreshCookie)
		if err != nil {
			return providerView{}, err
		}
	}
	p.Currency = strings.ToUpper(strings.TrimSpace(p.Currency))
	p.CustomCurrency = strings.TrimSpace(p.CustomCurrency)
	if p.Currency == "AUTO" {
		p.Currency = ""
	}
	if p.Currency != "" && p.Currency != "CUSTOM" && !slices.Contains(strings.Fields("USD EUR RUB CNY GBP JPY KRW INR UAH KZT BYN TRY AED SAR ILS CHF CAD AUD NZD SGD HKD TWD VND THB IDR MYR PHP BRL MXN PLN CZK SEK NOK DKK ZAR USDT USDC DAI BTC ETH TON SOL BNB TRX XRP LTC DOGE XMR BCH"), p.Currency) {
		return providerView{}, errors.New("Выберите валюту из списка или вариант «Своя валюта»")
	}
	if p.Currency == "CUSTOM" {
		if p.CustomCurrency == "" || utf8.RuneCountInString(p.CustomCurrency) > 24 || strings.IndexFunc(p.CustomCurrency, unicode.IsControl) >= 0 {
			return providerView{}, errors.New("Укажите своё обозначение валюты (до 24 символов, без переносов строк)")
		}
	} else {
		p.CustomCurrency = ""
	}
	if p.Name == "" || len(p.Name) > 200 {
		return providerView{}, errors.New("Укажите имя поставщика (до 200 символов)")
	}
	var err error
	p.URL, err = normalizeURL(p.URL)
	if err != nil {
		return providerView{}, err
	}
	if p.AuthMode != "bearer" && p.AuthMode != "cookie" {
		return providerView{}, errors.New("Выберите способ авторизации")
	}
	if p.UserID != "" {
		id, err := strconv.ParseUint(p.UserID, 10, 63)
		if err != nil || id == 0 {
			return providerView{}, errors.New("New-Api-User должен быть положительным числом")
		}
	}
	if len(p.Token) > 16384 || strings.ContainsAny(p.Token, "\r\n") {
		return providerView{}, errors.New("Скопируйте только значение токена или Cookie без переносов строк")
	}
	if p.AuthMode == "bearer" && len(p.Token) >= 7 && strings.EqualFold(p.Token[:7], "bearer ") {
		p.Token = strings.TrimSpace(p.Token[7:])
	}
	if p.AuthMode == "cookie" && len(p.Token) >= 7 && strings.EqualFold(p.Token[:7], "cookie:") {
		p.Token = strings.TrimSpace(p.Token[7:])
	}
	index := slices.IndexFunc(store.providers, func(item provider) bool { return item.ID == p.ID })
	if p.ID != "" && index < 0 {
		return providerView{}, errors.New("Поставщик не найден; обновите страницу")
	}
	if index >= 0 {
		old := store.providers[index]
		if p.Token == "" && p.URL == old.URL && p.AuthMode == old.AuthMode {
			p.Token = old.Token
		}
		if p.AutoRefresh && p.RefreshCookie == "" && old.AuthMode == "bearer" && p.URL == old.URL && p.UserID == old.UserID {
			p.RefreshCookie = old.RefreshCookie
			p.RefreshCookieHeader = old.RefreshCookieHeader
		}
		if p.Token == old.Token && p.URL == old.URL && p.AuthMode == old.AuthMode {
			p.TokenRefreshedAt = old.TokenRefreshedAt
			p.TokenExpiresAt = old.TokenExpiresAt
		}
		if p.URL == old.URL && p.Token == old.Token && p.UserID == old.UserID && p.AuthMode == old.AuthMode {
			p.Balance, p.LastError, p.AttemptedAt = old.Balance, old.LastError, old.AttemptedAt
		}
	} else {
		if len(store.providers) >= 100 {
			return providerView{}, errors.New("Можно добавить до 100 поставщиков")
		}
		id := make([]byte, 12)
		if _, err := rand.Read(id); err != nil {
			return providerView{}, errors.New("Не удалось создать ID поставщика")
		}
		p.ID = hex.EncodeToString(id)
	}
	if p.AutoRefresh && p.RefreshCookie == "" {
		return providerView{}, errors.New("Для автообновления нужна cookie new_api_refresh. При изменении URL или пользователя введите её заново")
	}
	if !p.AutoRefresh {
		p.RefreshCookie = ""
		p.RefreshCookieHeader = ""
	}
	if p.TokenExpiresAt == 0 && p.AuthMode == "bearer" {
		p.TokenExpiresAt = bearerExpiry(p.Token)
	}
	if p.Token == "" && !p.AutoRefresh {
		return providerView{}, errors.New("Укажите токен/cookie. При изменении URL или типа авторизации введите его заново")
	}
	next := slices.Clone(store.providers)
	if index < 0 {
		next = append(next, p)
	} else {
		next[index] = p
	}
	if err := store.persist(next); err != nil {
		return providerView{}, err
	}
	store.revisions[p.ID]++
	return store.view(p), nil
}

func (store *providerStore) remove(id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	index := slices.IndexFunc(store.providers, func(p provider) bool { return p.ID == id })
	if index < 0 {
		return errors.New("Поставщик не найден")
	}
	next := slices.Delete(slices.Clone(store.providers), index, index+1)
	if err := store.persist(next); err != nil {
		return err
	}
	store.revisions[id]++
	return nil
}

func (store *providerStore) refresh(ctx context.Context, id string) (providerView, int, error) {
	store.mu.Lock()
	index := slices.IndexFunc(store.providers, func(p provider) bool { return p.ID == id })
	if index < 0 {
		store.mu.Unlock()
		return providerView{}, 404, errors.New("Поставщик не найден")
	}
	if store.busy[id] {
		store.mu.Unlock()
		return providerView{}, 409, errors.New("Проверка баланса уже выполняется")
	}
	store.busy[id] = true
	p, revision := store.providers[index], store.revisions[id]
	store.mu.Unlock()
	// A single deadline covers renewal, one authorization retry and balance/status.
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	renewed := false
	var result *balance
	var errFetch error
	if p.AutoRefresh && p.AuthMode == "bearer" && needsBearerRefresh(p) {
		p, errFetch = store.renewBearer(ctx, id, revision)
		renewed = true
	}
	if errFetch == nil {
		result, errFetch = fetchBalance(ctx, p)
	}
	if errors.Is(errFetch, errAuthorization) && p.AutoRefresh && p.AuthMode == "bearer" && !renewed {
		p, errFetch = store.renewBearer(ctx, id, revision)
		if errFetch == nil {
			result, errFetch = fetchBalance(ctx, p)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.busy, id)
	index = slices.IndexFunc(store.providers, func(p provider) bool { return p.ID == id })
	if index < 0 || store.revisions[id] != revision {
		return providerView{}, 409, errors.New("Настройки изменились во время запроса. Повторите проверку")
	}
	now := time.Now().UTC()
	p.AttemptedAt, p.LastError = &now, ""
	if errFetch != nil {
		p.LastError = errFetch.Error()
	} else {
		p.Balance = result
	}
	next := slices.Clone(store.providers)
	next[index] = p
	if err := store.persist(next); err != nil {
		return providerView{}, 500, err
	}
	return store.view(p), 200, nil
}
