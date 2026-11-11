package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type balance struct {
	Quota       float64   `json:"quota"`
	Amount      float64   `json:"amount"`
	Unit        string    `json:"unit"`
	DisplayUnit string    `json:"display_unit,omitempty"`
	Warning     string    `json:"warning,omitempty"`
	CheckedAt   time.Time `json:"checked_at"`
}

type newAPIResponse struct {
	Success *bool           `json:"success"`
	Data    json.RawMessage `json:"data"`
	Code    string          `json:"code"`
}

var errAuthorization = errors.New("Доступ отклонён: обновите токен/cookie и проверьте New-Api-User")

type displaySettings struct {
	QuotaPerUnit   float64 `json:"quota_per_unit"`
	DisplayType    string  `json:"quota_display_type"`
	DisplayMoney   *bool   `json:"display_in_currency"`
	USDExchange    float64 `json:"usd_exchange_rate"`
	CustomSymbol   string  `json:"custom_currency_symbol"`
	CustomExchange float64 `json:"custom_currency_exchange_rate"`
}

func normalizeURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("Укажите URL сайта без логина, query-параметров и фрагмента")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return "", errors.New("Для поставщика нужен HTTPS URL")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.Path = strings.TrimSuffix(u.Path, "/v1")
	return strings.TrimRight(u.String(), "/"), nil
}

func fetchBalance(ctx context.Context, provider provider) (*balance, error) {
	client := &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, provider.URL+"/api/user/self", nil)
	if err != nil {
		return nil, errors.New("Некорректный URL поставщика")
	}
	if provider.AuthMode == "cookie" {
		req.Header.Set("Cookie", provider.Token)
	} else {
		req.Header.Set("Authorization", "Bearer "+provider.Token)
		// Keep browser challenge/device cookies from a complete refresh header,
		// but never send the HttpOnly refresh credential to /api/user/self.
		if auxiliary := withoutCookie(provider.RefreshCookieHeader, refreshCookieName); auxiliary != "" {
			req.Header.Set("Cookie", auxiliary)
		}
	}
	if provider.UserID != "" {
		req.Header.Set("New-Api-User", provider.UserID)
	}
	req.Header.Set("Accept", "application/json")
	self, err := getNewAPI(client, req)
	if err != nil {
		return nil, err
	}
	var user struct {
		Quota *float64 `json:"quota"`
	}
	if json.Unmarshal(self.Data, &user) != nil || user.Quota == nil || !finite(*user.Quota) {
		return nil, errors.New("В ответе /api/user/self нет числового поля data.quota")
	}
	result := &balance{Quota: *user.Quota, Amount: *user.Quota, Unit: "quota", CheckedAt: time.Now().UTC()}
	statusReq, err := http.NewRequestWithContext(ctx, http.MethodGet, provider.URL+"/api/status", nil)
	if err != nil {
		return nil, errors.New("Некорректный URL поставщика")
	}
	statusReq.Header.Set("Accept", "application/json")
	status, err := getNewAPI(client, statusReq)
	var settings displaySettings
	if err != nil || json.Unmarshal(status.Data, &settings) != nil {
		result.Warning = "Валютные настройки /api/status недоступны. Показана исходная квота"
		return result, nil
	}
	if settings.DisplayType == "TOKENS" || (settings.DisplayType == "" && settings.DisplayMoney != nil && !*settings.DisplayMoney) {
		result.Unit = "tokens"
		return result, nil
	}
	if settings.QuotaPerUnit <= 0 || !finite(settings.QuotaPerUnit) {
		result.Warning = "Поставщик не сообщил quota_per_unit. Показана исходная квота"
		return result, nil
	}
	amount, unit := result.Quota/settings.QuotaPerUnit, "units"
	switch settings.DisplayType {
	case "USD":
		unit = "USD"
	case "":
		result.Warning = "Поставщик не указал валюту. Показаны единицы баланса; обозначение можно выбрать в настройках"
	case "CNY":
		if settings.USDExchange <= 0 || !finite(settings.USDExchange) {
			result.Warning = "Не указан курс CNY. Показаны единицы баланса до пересчёта"
		} else {
			amount, unit = amount*settings.USDExchange, "CNY"
		}
	case "CUSTOM":
		if settings.CustomExchange <= 0 || !finite(settings.CustomExchange) || strings.TrimSpace(settings.CustomSymbol) == "" {
			result.Warning = "Не указана пользовательская валюта или курс. Показаны единицы баланса до пересчёта"
		} else {
			amount, unit = amount*settings.CustomExchange, settings.CustomSymbol
		}
	default:
		result.Warning = "Неизвестная валюта поставщика. Показаны единицы баланса; обозначение можно выбрать в настройках"
	}
	if !finite(amount) {
		result.Warning = "Некорректный коэффициент пересчёта. Показана исходная квота"
		return result, nil
	}
	result.Amount, result.Unit = amount, unit
	return result, nil
}

func getNewAPI(client *http.Client, req *http.Request) (newAPIResponse, error) {
	out, _, err := requestNewAPI(client, req)
	return out, err
}

func requestNewAPI(client *http.Client, req *http.Request) (newAPIResponse, []*http.Cookie, error) {
	var out newAPIResponse
	response, err := client.Do(req)
	if err != nil {
		return out, nil, errors.New("Не удалось связаться с поставщиком: проверьте URL, сеть и сертификат")
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == 401 || response.StatusCode == 403:
		return out, nil, errAuthorization
	case response.StatusCode == 429:
		return out, nil, errors.New("Поставщик ограничил частоту запросов. Повторите позже")
	case response.StatusCode >= 300 && response.StatusCode < 400:
		return out, nil, errors.New("Поставщик перенаправляет запрос. Укажите конечный URL сайта")
	case response.StatusCode < 200 || response.StatusCode >= 300:
		return out, nil, errors.New("API поставщика вернул ошибку. Проверьте URL и доступность API")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || json.Unmarshal(body, &out) != nil {
		return out, nil, errors.New("Получен некорректный ответ: ожидался JSON New API, возможна защита сайта")
	}
	if out.Success == nil || !*out.Success {
		if out.Code == "AUTH_TOKEN_EXPIRED" || out.Code == "AUTH_UNAUTHORIZED" {
			return out, nil, errAuthorization
		}
		return out, nil, errors.New("New API отклонил запрос: проверьте токен/cookie и New-Api-User")
	}
	return out, response.Cookies(), nil
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
