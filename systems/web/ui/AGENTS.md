# Browser app

Use repository-root Make targets and the pinned Corepack/pnpm toolchain. Read [README.md](README.md)
for the full dependency allowlists and contract workflow.

- `src/domain` and `src/shared` are deterministic ES2023-only code: no browser/Node APIs, clocks,
  randomness, timers or input mutation. Keep public state and collections readonly.
- `src/application` orchestrates through ports; `src/infrastructure` implements I/O adapters.
  Components depend inward on application/domain, never on adapters. `src/main.tsx` wires concrete
  adapters into the application.
- Use canonical literal imports and explicit `import type`. Preserve cycle and layer guards.
  Validate unknown JSON before narrowing; type assertions, `any` and non-null assertions are
  forbidden.
- Colocate component CSS Modules. Reserve `src/styles.css` for theme tokens, base styles and
  accessibility; reuse Catppuccin Mocha/Latte, Recursive, rem typography and the 4px spacing scale.
- Keep copy concise and keyboard access intact. Honor reduced-motion changes while the app is open;
  preserve the reader's position when paging history or when live content grows.
- `worker/sw.ts` uses WebWorker types, not DOM types. Cache only the compiled shell; keep messages,
  cookies and raw keys out of browser storage. Only the opaque non-exportable session CryptoKey and
  its public binding ID may persist in IndexedDB; use the shared proof adapter for signed network
  fetches.
- For UI code, run `make ui-lint`, `make ui-test-coverage` and `make ui-e2e`, plus the root
  `make verify` gate. Preserve lint rules and coverage floors; architecture probes must still reject
  forbidden dependencies, effects and unsafe types.
