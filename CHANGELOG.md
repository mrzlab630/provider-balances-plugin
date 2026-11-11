# Changelog

## 0.4.1 — 2026-11-11

- Use the plugin's own GitHub repository in metadata and its public Go module path.
- Document building, first installation, updates, rollback, supported providers
  and credential storage without referring to a specific server deployment.
- Allow installation helpers to select the CLIProxyAPI directory and built
  library; let the first-install helper select the active configuration.
- Create missing plugin and backup directories during installation.

## 0.4.0 — 2026-11-11

- Add AnyModel Cookie authentication through `GET /api/me`.
- Show `balanceUsd` as the money amount and `balance` as a separate token count,
  taking both values directly from the provider response.
- Preserve cached balances on session or network errors, including zero balances.
- Keep legacy records compatible with New API and bind saved credentials to
  the selected provider type.
- Ignore user details and API keys returned by AnyModel.

## 0.3.0

- Initial imported implementation: New API balances, Bearer/Cookie authentication,
  optional Bearer renewal, currency labels, protected management routes and
  the Russian-language interface.
