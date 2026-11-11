package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

const (
	apiTypeNewAPI   = "new-api"
	apiTypeAnyModel = "anymodel"
)

func normalizedAPIType(value string) string {
	if value == "" {
		return apiTypeNewAPI
	}
	return value
}

func validateAnyModelCookie(header string) error {
	req := &http.Request{Header: http.Header{"Cookie": {header}}}
	session, err := req.Cookie("am_session")
	if err != nil || session.Value == "" || session.Valid() != nil {
		return errors.New("Для AnyModel скопируйте заголовок Cookie из запроса /api/me с непустой cookie am_session")
	}
	return nil
}

func fetchAnyModelBalance(ctx context.Context, provider provider) (*balance, error) {
	if provider.AuthMode != "cookie" {
		return nil, errors.New("Для AnyModel нужна авторизация Cookie сессии")
	}
	client := &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, provider.URL+"/api/me", nil)
	if err != nil {
		return nil, errors.New("Некорректный URL AnyModel")
	}
	req.Header.Set("Cookie", provider.Token)
	req.Header.Set("Accept", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("Не удалось связаться с AnyModel: проверьте URL, сеть и сертификат")
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusUnauthorized:
		return nil, errors.New("Сессия AnyModel истекла или отозвана. Войдите в личный кабинет и обновите Cookie с am_session")
	case response.StatusCode == http.StatusForbidden:
		return nil, errors.New("AnyModel отклонил доступ. Обновите Cookie из запроса /api/me; проверьте am_session и защитные cookies сайта")
	case response.StatusCode == http.StatusTooManyRequests:
		return nil, errors.New("AnyModel ограничил частоту запросов. Повторите позже")
	case response.StatusCode >= 300 && response.StatusCode < 400:
		return nil, errors.New("AnyModel перенаправляет запрос. Проверьте URL сайта и обновите Cookie сессии")
	case response.StatusCode < 200 || response.StatusCode >= 300:
		return nil, errors.New("API AnyModel вернул ошибку. Проверьте URL и доступность API")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	// Decode only balances: /api/me also contains private user data and API keys.
	var data struct {
		Balance    *int64   `json:"balance"`
		BalanceUSD *float64 `json:"balanceUsd"`
	}
	if err != nil || len(raw) > 1<<20 || json.Unmarshal(raw, &data) != nil {
		return nil, errors.New("Некорректный ответ AnyModel: ожидался JSON /api/me, возможна защита сайта")
	}
	if data.Balance == nil || data.BalanceUSD == nil || !finite(*data.BalanceUSD) {
		return nil, errors.New("В ответе AnyModel /api/me нет числовых полей balance и balanceUsd")
	}
	return &balance{
		Quota: float64(*data.Balance), Amount: *data.BalanceUSD, Unit: "USD",
		Tokens: data.Balance, CheckedAt: time.Now().UTC(),
	}, nil
}
