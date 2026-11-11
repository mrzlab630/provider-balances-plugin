package main

import (
	"context"
	"embed"
	"encoding/json"
	"net/http"
	"net/url"
)

//go:embed web/*
var webAssets embed.FS

type managementRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Query   url.Values
	Body    []byte
}

type managementResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

func jsonResponse(status int, data any) managementResponse {
	body, err := json.Marshal(data)
	if err != nil {
		status, body = 500, []byte("{\"error\":\"Response encoding failed\"}")
	}
	return managementResponse{StatusCode: status, Body: body, Headers: http.Header{
		"Content-Type":           {"application/json; charset=utf-8"},
		"Cache-Control":          {"no-store"},
		"X-Content-Type-Options": {"nosniff"},
	}}
}

func handleManagement(store *providerStore, req managementRequest) managementResponse {
	asset, mime := "", ""
	if req.Method == http.MethodGet {
		switch req.Path {
		case resourcePath + "/balances":
			asset, mime = "index.html", "text/html; charset=utf-8"
		case resourcePath + "/app.js":
			asset, mime = "app.js", "text/javascript; charset=utf-8"
		case resourcePath + "/styles.css":
			asset, mime = "styles.css", "text/css; charset=utf-8"
		}
	}
	if asset != "" {
		body, err := webAssets.ReadFile("web/" + asset)
		if err != nil {
			return jsonResponse(500, map[string]string{"error": "Page asset unavailable"})
		}
		return managementResponse{StatusCode: 200, Body: body, Headers: http.Header{
			"Content-Type": {mime}, "Cache-Control": {"no-store"},
			"X-Content-Type-Options": {"nosniff"}, "Referrer-Policy": {"no-referrer"},
			"Content-Security-Policy": {"default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"},
		}}
	}
	if store == nil {
		return jsonResponse(503, map[string]string{"error": "Plugin is not configured"})
	}
	if len(req.Body) > 64<<10 {
		return jsonResponse(413, map[string]string{"error": "Request too large"})
	}
	switch {
	case req.Method == http.MethodGet && req.Path == apiPath+"/providers":
		return jsonResponse(200, map[string]any{"providers": store.list()})
	case req.Method == http.MethodPut && req.Path == apiPath+"/providers":
		// Decode only editable fields; cached balances are owned by the server.
		var input provider
		if json.Unmarshal(req.Body, &input) != nil {
			return jsonResponse(400, map[string]string{"error": "Некорректные настройки поставщика"})
		}
		view, err := store.save(provider{ID: input.ID, Name: input.Name, URL: input.URL, APIType: input.APIType, Token: input.Token, AuthMode: input.AuthMode, UserID: input.UserID, Currency: input.Currency, CustomCurrency: input.CustomCurrency, AutoRefresh: input.AutoRefresh, RefreshCookie: input.RefreshCookie})
		if err != nil {
			return jsonResponse(400, map[string]string{"error": err.Error()})
		}
		return jsonResponse(200, map[string]any{"provider": view})
	case req.Method == http.MethodDelete && req.Path == apiPath+"/providers":
		if err := store.remove(req.Query.Get("id")); err != nil {
			return jsonResponse(400, map[string]string{"error": err.Error()})
		}
		return jsonResponse(200, map[string]bool{"success": true})
	case req.Method == http.MethodPost && req.Path == apiPath+"/refresh":
		var input provider
		if json.Unmarshal(req.Body, &input) != nil || input.ID == "" {
			return jsonResponse(400, map[string]string{"error": "Укажите поставщика"})
		}
		view, status, err := store.refresh(context.Background(), input.ID)
		if err != nil {
			return jsonResponse(status, map[string]string{"error": err.Error()})
		}
		return jsonResponse(200, map[string]any{"provider": view})
	default:
		return jsonResponse(404, map[string]string{"error": "Not found"})
	}
}
