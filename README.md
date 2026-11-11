# Provider Balances for CLIProxyAPI

A local C ABI plugin with a Russian-language page for New API account balances.
Version 0.3.0, verified against CLIProxyAPI 8.0.31.

Open [Provider balances](https://ai.yokinaboy.com/v0/resource/plugins/provider-balances/balances).
Enter the CLIProxyAPI management key, select Settings, and add a provider name,
the provider website URL, and a dashboard credential. Click the wallet on the
Balances tab to refresh that provider. The page asks for the management key again
after a reload; it keeps the key only in browser memory.

## New API authentication

1. Sign in to the provider's dashboard.
2. Open browser developer tools, select Network, and reload the page.
3. Find the request to /api/user/self.
4. Copy the value of its Authorization header and select Bearer authentication.
   If it uses a browser session instead, select Cookie and copy the Cookie header.
5. If present, copy the New-Api-User header into the optional user ID field.

Use the website URL, without /console or /api/user/self. A trailing /v1 is
removed automatically. Provider URLs require HTTPS, except localhost for testing.
Bearer session tokens can be renewed automatically as described below. Expired
or revoked refresh sessions need manual replacement. A model API key is not
necessarily authorized to read the dashboard account balance.

## Automatic Bearer renewal

For providers implementing New API's POST /api/user/auth/refresh, enable
"Автоматически обновлять Bearer-токен" and paste the value of the
new_api_refresh cookie. It is an HttpOnly cookie scoped to /api/user/auth, so it
may be absent from Application → Cookies and document.cookie. In DevTools →
Network, open POST /api/user/auth/refresh and copy its complete Request Headers
→ Cookie value. The complete header is accepted, including auxiliary cookies
such as a provider's browser challenge/device cookie; the refresh credential is
still required. The public new_api_has_session=1 marker is not a credential and
cannot refresh a token.

Use a separate provider login session for the plugin. The refresh cookie rotates
on use; sharing the same session with a browser that also renews it can cause
refresh-token replay detection and revoke the session. Signing out of that
session or revoking it at the provider also stops renewal.

The initial Bearer token is optional when renewal is enabled. On a wallet click,
the server obtains a token if none is saved or the known expiry is within one
minute. Otherwise it requests the balance normally and renews/retries once if
authentication is rejected. Expiry is read from the JWT exp scheduling hint or
the provider's access_expires_at; the provider still verifies authentication.
The mechanism runs on demand, without background keepalive requests.

The new access token and any replacement Set-Cookie new_api_refresh value are
saved atomically before requesting the balance. They survive reloads and failed
balance requests. One provider cannot start overlapping renewals. Saving edits
during cookie rotation is rejected to avoid losing a newly issued credential.
All operations for a wallet click share a 45-second deadline.

Both credentials stay on the server in the existing 0600 data file. Management
responses return only has_token, has_refresh_cookie, expiry and renewal time.
Blank fields preserve saved credentials when the provider identity matches;
changing the URL or user ID requires a new refresh cookie. Disabling renewal
removes the saved refresh cookie while retaining the current Bearer token.

If the refresh session expires, fails or is revoked, the last successful balance
is kept and an error appears inside that provider's card. Re-enter a current
new_api_refresh cookie to reconnect. Providers using other refresh mechanisms
need a provider-specific integration; an expired Bearer alone cannot issue a new
token.

## API and currency

The plugin reads data.quota from GET /api/user/self, using the selected dashboard
credential. It reads the public GET /api/status without credentials to obtain
quota_per_unit and currency display settings. The default is Auto: use the
provider's reported USD, CNY, custom currency, or token display. Missing or unknown
currency settings never assume USD. If quota_per_unit is available without a
known currency, the plugin shows generic balance units; if the conversion factor
is unavailable, it shows raw quota with an explanation.

Each provider has a separate currency selector with 35 fiat currencies and
14 cryptocurrencies/stablecoins, including RUB, EUR, CNY, USDT, USDC, BTC, ETH,
TON and SOL. The Custom option accepts a symbol, code, word, or emoji up to
24 characters. Symbols are displayed on balance cards; hovering a known symbol
shows its currency code and name. Crypto and custom balances show up to eight
decimal places.

Manual currency selection changes only the displayed label. It preserves the
provider's amount and conversion settings and does not exchange currencies. Raw
quota and token counts retain their own units. Changing the label immediately
updates the cached balance; switching back to Auto restores the original label.
Existing provider records without a currency field use Auto.

While a wallet request runs, a spinner covers only that provider's card and
blocks mouse and keyboard interaction with its contents. Other cards remain
usable. There is no separate refresh status line or global refresh notification.
Provider/API/network errors appear inside the affected card, preserving the
last successful amount. Its timestamp is available by hovering the amount.

Requests originate on the CLIProxyAPI server, avoiding browser CORS restrictions.
Redirects are rejected so credentials are never forwarded to a redirected URL.
Provider failures preserve the last successful balance with its timestamp and
an error message. Updating credentials resets the saved balance. Requests that
finish after an edit or removal cannot overwrite the newer settings.

## Installation

The current deployment uses:

- Active config: ../.video-gateway-switch/config-core-8318.yaml
- Library: ../plugins/linux/amd64/provider-balances-v0.1.0.so
  (the installed filename is retained to avoid duplicate plugin discovery)
- Data: ../data/provider-balances.json, created on first provider save
- Service: systemctl --user restart cliproxyapi.service

Configuration block under plugins.configs:

    provider-balances:
        enabled: true
        data_file: /home/630monster/cliproxyapi/data/provider-balances.json

The server stores credentials in this file as plaintext with mode 0600; newly
created parent directories have mode 0700. Back up this file as a secret.
List/save API responses expose only has_token, never the stored credential.
Blank credentials in the editor preserve the saved value when the URL and
authentication mode remain the same. A new URL requires re-entering credentials.

The page and its static assets are public. Provider data and mutations are
protected by the host's management authentication. No provider data is embedded
in the public page.

The first installation made a backup in
../operations/provider-balances-20261019T130947Z/. To disable the plugin, set
plugins.configs.provider-balances.enabled to false in the active configuration
and restart the service. Do not restore the entire old config after unrelated
configuration changes.

The plugin advertises its page through the standard resource menu registration.
The direct URL works independently of management-panel menu support. Metadata
links to the host CLIProxyAPI project to satisfy the host's required repository
field; this local plugin has not been published to that repository.

## Build and verify

From this directory:

    go test -race ./...
    node --check web/app.js
    go build -buildmode=c-shared -o /tmp/provider-balances-v0.1.0.so .

The staging/installed filename is retained for compatibility; the plugin reports
version 0.3.0. To update this existing installation after verification:

    python3 update-local.py
    systemctl --user restart cliproxyapi.service

Requires Go 1.26+, CGO and a C compiler. The UI is embedded in the library.
Stop or restart the host when replacing an installed shared library; do not
overwrite a loaded library in place. Stage a new file and atomically rename it.

Validation completed: race-enabled tests, shared-library build, integration with
the installed CLIProxyAPI binary, protected API, provider CRUD, balance refresh,
and Chromium desktop/mobile flows, including cookie-only enrollment, renewal
before expiry, one authorization retry, cookie rotation, secret masking,
failure handling and concurrent requests. The end-to-end provider was a local New API
test server with known quota, not a real provider account. Provider-specific
forks and anti-bot protection can require additional compatibility work.
