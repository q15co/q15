# Web tier

Follow the root `AGENTS.md`; run Make targets from the repository root.

- The agent owns durable transcripts and agent credentials. Keep this tier's state ephemeral and use
  an absolute `unix://` bridge target with the startup protocol handshake.
- Keep authorization behind `gate.Authorizer.RequireScope` and `gate.WithPrincipal`. Identity comes
  from authorization; the owner-token gate is temporary. Protect every route except `/healthz`.
- Require the exact configured Origin. Native WebSocket handshakes can omit Fetch Metadata; reject
  conflicting metadata when supplied.
- Encode int64 values as decimal JSON strings. Transcript cursors and session event indexes are
  distinct; `client_msg_id` provides correlation, not send idempotency.
- Go JSON types and fixtures are canonical. Update browser copies and generated types together; see
  [ui/README.md](ui/README.md) for regeneration and run `make ui-fixtures-check`.
- Only `internal/assets/dist/.gitkeep` is tracked. Runnable builds need the UI bundle: use
  `make build-web` or `make build-web-image`; `make ui-clean` restores the embed sentinel.
- Run `make test-web` and the root final gate. For browser contract changes, also run the checks in
  [ui/AGENTS.md](ui/AGENTS.md): Go-only changes can skip browser CI.
