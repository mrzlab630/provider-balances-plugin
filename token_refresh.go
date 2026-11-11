package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const refreshCookieName = "new_api_refresh"

// Preserve rotated credentials before requesting the balance. An unrelated edit
// cannot discard a one-time refresh cookie while its rotation is in progress.
func (store *providerStore) renewBearer(ctx context.Context, id string, revision uint64) (provider, error) {
	store.mu.Lock()
	index := slices.IndexFunc(store.providers, func(p provider) bool { return p.ID == id })
	if index < 0 || store.revisions[id] != revision {
		store.mu.Unlock()
		return provider{}, errors.New("Настройки изменились во время запроса. Повторите проверку")
	}
	p := store.providers[index]
	store.renewing[id] = true
	store.mu.Unlock()
	updated, err := refreshBearer(ctx, p)
	store.mu.Lock()
	defer store.mu.Unlock()
	defer delete(store.renewing, id)
	index = slices.IndexFunc(store.providers, func(p provider) bool { return p.ID == id })
	if index < 0 || store.revisions[id] != revision {
		return p, errors.New("Настройки изменились во время запроса. Повторите проверку")
	}
	if err != nil {
		return p, err
	}
	next := slices.Clone(store.providers)
	next[index] = updated
	if err := store.persist(next); err != nil {
		// Keep the rotated pair in memory so the next attempt never replays the old cookie.
		store.providers = next
		return updated, errors.New("Токен обновлён, но не удалось сохранить его на диск. Повторите запрос и проверьте доступ к файлу настроек")
	}
	return updated, nil
}

func normalizeRefreshCookie(raw string) (string, error) {
	value, _, err := normalizeRefreshCookieInput(raw)
	return value, err
}

// normalizeRefreshCookieInput accepts either the value of new_api_refresh or
// the complete Cookie request header. The latter is useful for providers
// protected by a browser-bound challenge (for example cf_clearance): the
// refresh endpoint may require the auxiliary cookies as well. The refresh
// credential is still required and is extracted separately for rotation.
func normalizeRefreshCookieInput(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 16384 || strings.ContainsAny(raw, "\r\n") {
		return "", "", errors.New("Скопируйте cookie new_api_refresh или заголовок Cookie без переносов строк")
	}
	if len(raw) >= 7 && strings.EqualFold(raw[:7], "cookie:") {
		raw = strings.TrimSpace(raw[7:])
	}
	header := ""
	if strings.ContainsAny(raw, "=;") {
		req := &http.Request{Header: http.Header{"Cookie": {raw}}}
		cookie, err := req.Cookie(refreshCookieName)
		if err != nil {
			if strings.Contains(raw, "new_api_has_session=") {
				return "", "", errors.New("new_api_has_session — только признак сессии. Нужна скрытая HttpOnly cookie new_api_refresh из заголовка Cookie запроса /api/user/auth/refresh")
			}
			return "", "", errors.New("В заголовке Cookie не найден new_api_refresh")
		}
		raw = cookie.Value
		cookies := req.Cookies()
		if len(cookies) == 0 {
			return "", "", errors.New("Укажите корректный заголовок Cookie")
		}
		parts := make([]string, 0, len(cookies))
		for _, item := range cookies {
			if item.Name == "" || item.Value == "" || item.Valid() != nil {
				return "", "", errors.New("Укажите корректный заголовок Cookie")
			}
			parts = append(parts, item.Name+"="+item.Value)
		}
		header = strings.Join(parts, "; ")
	}
	cookie := &http.Cookie{Name: refreshCookieName, Value: raw}
	if raw == "" || strings.ContainsAny(raw, " \t") || cookie.Valid() != nil {
		return "", "", errors.New("Укажите корректное значение cookie new_api_refresh")
	}
	if header == "" {
		header = cookie.Name + "=" + cookie.Value
	}
	return raw, header, nil
}

// JWT claims are only scheduling hints. Authentication is always checked upstream.
func bearerExpiry(token string) int64 {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Exp <= 0 {
		return 0
	}
	return claims.Exp
}

func needsBearerRefresh(p provider) bool {
	return p.Token == "" || (p.TokenExpiresAt > 0 && p.TokenExpiresAt <= time.Now().Add(time.Minute).Unix())
}

func refreshBearer(ctx context.Context, p provider) (provider, error) {
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL+"/api/user/auth/refresh", strings.NewReader("{}"))
	if err != nil {
		return p, errors.New("Некорректный URL поставщика")
	}
	origin, _ := url.Parse(p.URL)
	req.Header.Set("Origin", origin.Scheme+"://"+origin.Host)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if p.RefreshCookieHeader != "" {
		req.Header.Set("Cookie", p.RefreshCookieHeader)
	} else {
		req.AddCookie(&http.Cookie{Name: refreshCookieName, Value: p.RefreshCookie})
	}
	out, cookies, err := requestNewAPI(client, req)
	if errors.Is(err, errAuthorization) {
		return p, errors.New("Сессия автообновления истекла или отозвана. Войдите к поставщику и обновите cookie new_api_refresh в настройках")
	}
	if err != nil {
		return p, fmt.Errorf("Не удалось обновить Bearer-токен через /api/user/auth/refresh: %w", err)
	}
	var data struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresAt   int64  `json:"access_expires_at"`
	}
	if json.Unmarshal(out.Data, &data) != nil || data.AccessToken == "" || len(data.AccessToken) > 16384 || strings.ContainsAny(data.AccessToken, " \t\r\n") || (data.TokenType != "" && !strings.EqualFold(data.TokenType, "Bearer")) {
		return p, errors.New("Поставщик не вернул корректный data.access_token при автообновлении")
	}
	if data.ExpiresAt == 0 {
		data.ExpiresAt = bearerExpiry(data.AccessToken)
	}
	if data.ExpiresAt < 0 || (data.ExpiresAt > 0 && data.ExpiresAt <= time.Now().Unix()) {
		return p, errors.New("Поставщик выдал уже истёкший Bearer-токен")
	}
	for _, cookie := range cookies {
		if cookie.Name != refreshCookieName {
			continue
		}
		if cookie.Value == "" || cookie.MaxAge < 0 || (!cookie.Expires.IsZero() && !cookie.Expires.After(time.Now())) {
			return p, errors.New("Поставщик завершил сессию автообновления. Обновите cookie new_api_refresh")
		}
		value, err := normalizeRefreshCookie(cookie.Value)
		if err != nil {
			return p, errors.New("Поставщик вернул некорректную cookie автообновления")
		}
		p.RefreshCookie = value
		p.RefreshCookieHeader = replaceRequestCookie(p.RefreshCookieHeader, refreshCookieName, value)
	}
	now := time.Now().UTC()
	p.Token, p.TokenExpiresAt, p.TokenRefreshedAt = data.AccessToken, data.ExpiresAt, &now
	return p, nil
}

// replaceRequestCookie updates one cookie in a request Cookie header while
// retaining auxiliary cookies that may be required by an upstream WAF.
func replaceRequestCookie(header, name, value string) string {
	if header == "" {
		return name + "=" + value
	}
	req := &http.Request{Header: http.Header{"Cookie": {header}}}
	cookies := req.Cookies()
	parts := make([]string, 0, len(cookies)+1)
	replaced := false
	for _, cookie := range cookies {
		if cookie.Name == name {
			parts = append(parts, name+"="+value)
			replaced = true
			continue
		}
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	if !replaced {
		parts = append(parts, name+"="+value)
	}
	return strings.Join(parts, "; ")
}

func withoutCookie(header, name string) string {
	if header == "" {
		return ""
	}
	req := &http.Request{Header: http.Header{"Cookie": {header}}}
	parts := make([]string, 0, len(req.Cookies()))
	for _, cookie := range req.Cookies() {
		if cookie.Name == name {
			continue
		}
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}
