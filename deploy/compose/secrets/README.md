# Compose Secrets

This directory holds local secret material used by the Compose stack.

Rules:

- Files without a `.example` suffix are git-ignored and should contain your local secret material.
- `*.example` files are tracked templates with placeholder values.
- Run `make compose-secrets-init` to create missing local secret files from the templates.
- Edit the local files, not the `*.example` templates.
