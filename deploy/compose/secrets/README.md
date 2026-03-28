# Compose Secrets

This directory holds local secret material for the checked-in Compose examples.

Rules:

- Files without a `.example` suffix are git-ignored and should contain your local secret material.
- `*.example` files are tracked templates with placeholder values.
- Run `make compose-secrets-init` from the repo root to create missing local secret files from the
  templates.
- Edit the local files, not the `*.example` templates.
- The generic tracked templates are:
  - `moonshot_api_key.example`
  - `q15_telegram_token.example`
  - `github_token.example`
  - `q15_auth_json.example`
