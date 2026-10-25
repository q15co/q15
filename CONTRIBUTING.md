# Contributing to q15

Issues and pull requests are welcome. Small and focused beats large and sweeping: an 80-line change
I can read in one sitting gets merged, a 900-line one usually gets a question instead.

## Before you open a pull request

Run the gate locally:

```bash
make verify
```

Postgres history tests run when `Q15_TEST_POSTGRES_URL` points to a disposable database. Run
`make test-history-postgres` to create one automatically and exercise history constraints, indexing
and runtime integration. Ordinary `make verify` skips these database tests when that variable is
absent; CI supplies its own Postgres service. Never point tests at an agent's memory database.

If your change touches the browser UI, also run `make test-web`. A pull request that arrives green
gets reviewed much faster than one that arrives red.

## Sign your commits

q15 takes contributions under the [Developer Certificate of Origin 1.1][dco]. It is a statement, not
a contract: by adding a `Signed-off-by` line you certify that you wrote the change, or that you
otherwise have the right to submit it, and that you are submitting it under this project's licence.

```bash
git commit -s -m "fix(agent): describe the change"
```

That produces:

```text
Signed-off-by: Your Name <you@example.com>
```

There is no CLA to sign, no form to fill in and no email to send. Use your real name and a real
address.

## Licence

q15 is released under the MIT licence, and contributions come in under the same terms. Inbound
equals outbound: what you send us is available to everyone on the same basis as the rest of the
project. See [LICENSE](LICENSE).

By contributing you agree that the project may distribute your contribution under the MIT licence.
You keep the copyright to your work; contributing does not transfer it.

## Style

- Go: match the surrounding code. `make fmt` and `make lint` are the arbiter, not taste.
- SQL: the pinned SQLFluff formatter and linter use the Postgres dialect, a 100-character line
  limit, uppercase keywords and lowercase identifiers, functions and types. Use
  `make fmt FILES='path/to/query.sql'` and `make lint-changed FILES='path/to/query.sql'`. Write sqlc
  named parameters as `sqlc.arg('parameter_name')`. Keep result columns in their intended order;
  that order also defines generated Go row types. After changing history queries, run
  `make memory-generate`. `make verify` checks all tracked SQL files and generated code;
  `make test-sql` exercises the formatter and linter through the same public targets.
- Documentation: plain language, second person, and no claim without a measurement behind it.
- Tests: a fix is a fix when a test fails without it.

[dco]: https://developercertificate.org/
