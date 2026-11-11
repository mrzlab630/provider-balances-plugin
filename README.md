# Provider Balances for CLIProxyAPI

A C ABI plugin with a Russian-language page for New API and AnyModel account balances.
Version **0.4.1**, tested with CLIProxyAPI **8.0.31** on Linux amd64.

Source: [mrzlab630/provider-balances-plugin](https://github.com/mrzlab630/provider-balances-plugin).
See [CHANGELOG.md](CHANGELOG.md) for version history.

After installation, open `/v0/resource/plugins/provider-balances/balances`
on your CLIProxyAPI server. The page is also registered in the host resource menu.
Enter the CLIProxyAPI management key, select Settings, and add a provider name,
the provider website URL, and a dashboard credential. Click the wallet on the
Balances tab to refresh that provider. The page asks for the management key again
after a reload; it keeps the key only in browser memory.

Select the provider type when adding an account. Existing records without an
api_type field remain New API providers; their saved credentials and balances
continue to work. Changing a provider's API type requires re-entering its
credential and clears the cached balance.

## AnyModel

Select **AnyModel**, use https://anymodel.org as the website URL, and paste the
complete Cookie request header from GET /api/me in the signed-in dashboard
(DevTools → Network). It must include a nonempty am_session cookie. Auxiliary
cookies used by the site's browser protection can be included in the same
header. The optional Cookie: prefix is accepted.

The plugin reads balanceUsd directly as USD and balance as the remaining integer
token count. Both appear on the card: the money amount first and a separate
token line below it. No fixed token-to-dollar ratio is assumed, and the debt
field is not subtracted from either returned balance. Both values must be
present; zero and negative balances are supported. Only these two balance
fields are decoded from the response: user details and model API keys returned
by /api/me are never saved or returned by the plugin.

Cookie authentication is required. New-Api-User and New API's Bearer renewal
settings are hidden and are not used for AnyModel. There is no confirmed
AnyModel session-renewal integration: after expiry or revocation, sign in again
and replace the saved Cookie. Errors preserve the last successful USD and token
balances, including their timestamp. Redirects are rejected and response bodies
are never included in error messages.

The management API uses api_type: "anymodel" (or "new-api" for New API).
Successful AnyModel balances contain amount, unit: "USD", and tokens; quota
mirrors the raw token count. Manual currency selection still changes only the
money label and never the token count.

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

For New API, the plugin reads data.quota from GET /api/user/self, using the
selected dashboard credential. It reads the public GET /api/status without
credentials to obtain quota_per_unit and currency display settings. The default is Auto: use the
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

Requires a CLIProxyAPI build with C ABI plugin support. Version 8.0.31 on
Linux amd64 has been verified. Building requires Go 1.26+, CGO and a C compiler;
Node.js is used only to check the embedded JavaScript.

Clone and build:

```sh
git clone https://github.com/mrzlab630/provider-balances-plugin.git
cd provider-balances-plugin
go test -race ./...
node --check web/app.js
go build -buildmode=c-shared -o /tmp/provider-balances-v0.1.0.so .
```

The UI is embedded in the library; no frontend dependency installation is
needed. The `v0.1.0` library filename is retained for existing installations;
the plugin metadata reports **0.4.1**. Keep only one copy of the plugin in the
host's discovery directory.

For a first installation, copy the built library to
`plugins/linux/amd64/provider-balances-v0.1.0.so` inside the CLIProxyAPI
installation. Enable the plugin in the **active** host configuration, preserving
other plugin entries and replacing the example absolute data path:

```yaml
plugins:
    enabled: true
    dir: plugins
    configs:
        provider-balances:
            enabled: true
            data_file: /path/to/cliproxyapi/data/provider-balances.json
```

Restart your CLIProxyAPI process or service after installing the library. For
example, if it is managed by a user service named `cliproxyapi.service`:

```sh
systemctl --user restart cliproxyapi.service
```

Configure the host's management authentication according to your CLIProxyAPI
installation. Use that management key to sign in to the plugin page.

### Optional installation helpers

The helper scripts accept explicit paths, so the repository and the CLIProxyAPI
installation do not have to share a parent directory. `--root` defaults to the
parent of the repository and `--source` defaults to the build path shown above.

For a first installation, `install-local.py` requires Python 3 and PyYAML. It
expects an existing enabled `plugins` block, `dir: plugins`, and a `configs`
mapping using the four-space indentation shown above. It backs up the active
configuration, adds only the plugin entry and installs the library:

```sh
python3 install-local.py --root /path/to/cliproxyapi --config /path/to/active-config.yaml
```

`--config` defaults to `ROOT/config.yaml`. For a different configuration layout,
use the manual installation steps instead of this helper.

For an existing installation, build the updated library and run:

```sh
python3 update-local.py --root /path/to/cliproxyapi
systemctl --user restart cliproxyapi.service
```

The updater needs only Python's standard library. It creates a dated backup
under `ROOT/operations/`, including the existing library and provider data, then
atomically replaces the library. It does not change the host configuration or
provider data. The service restart is a separate step.

Never overwrite a loaded shared library in place. Stage a new file and
atomically rename it, or stop the host before replacing the library.

## Data and access

The server stores provider credentials and cached balances in the configured
`data_file`. Credentials are plaintext, with file mode 0600; newly created
parent directories have mode 0700. Back up this file as a secret.
List/save API responses expose only has_token, never the stored credential.
Blank credentials in the editor preserve the saved value when the provider type,
URL and authentication mode remain the same. A new URL or API type requires
re-entering credentials.

The page and its static assets are public. Provider data and mutations are
protected by the host's management authentication. No provider data is embedded
in the public page.

To disable the plugin, set `plugins.configs.provider-balances.enabled` to
`false` in the active host configuration and restart the service. Restore a
saved library to roll back a plugin update; keep the current host configuration
when it contains unrelated changes.

The direct page URL works independently of management-panel menu support.
Plugin metadata links to this plugin's own GitHub repository.

Runtime data, credentials, local configurations, logs, backups, HAR captures,
compiled libraries and caches are excluded by `.gitignore`. Keep the actual
provider data outside the source checkout. Tests use synthetic credentials and
responses; real provider credentials are not needed to run them.

## Verification

From the repository directory:

```sh
go test -race ./...
node --check web/app.js
go build -buildmode=c-shared -o /tmp/provider-balances-v0.1.0.so .
```

Validation completed: race-enabled tests, shared-library build, integration with
the installed CLIProxyAPI binary, protected API, provider CRUD, balance refresh,
and Chromium desktop/mobile flows, including cookie-only enrollment, renewal
before expiry, one authorization retry, cookie rotation, secret masking,
failure handling and concurrent requests. The end-to-end provider was a local New API
test server with known quota, not a real provider account. Provider-specific
forks and anti-bot protection can require additional compatibility work.

AnyModel tests use synthetic responses and cookies. They cover both balances,
zero and negative amounts, direct USD mapping without recalculation, session
failures, invalid responses, redirect rejection, secret masking, persistence,
provider-type changes, and backward compatibility with New API records.
The shared library was also loaded into an isolated CLIProxyAPI 8.0.31 instance
and checked in Chromium on desktop and mobile viewports,
including adding a provider, masked credentials, blank saves, reloading,
expired-session errors, zero balances, and both displayed amounts.
No real AnyModel account was contacted by these tests.
