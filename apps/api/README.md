# API

Minimal Go API scaffold using chi.

## Run

```bash
cd apps/api
go run ./cmd/server
```

Optional:

```bash
API_ADDR=":9090" go run ./cmd/server
```

## Test

```bash
nix develop --command make test-api lint-api sql-check sql-generated-drift-check
```

Static CockroachDB queries live in adapter `queries/*.sql` files. After editing
one, run `nix develop --command make sql-generate`, then rerun the checks above.
The drift check regenerates in a temporary copy and does not rewrite your worktree.
With the local migrated CockroachDB running, set `SCYTHE_DATABASE_URL` and run
`nix develop --command make sql-live-drift-test sql-check-live` for catalog
verification and its missing-column regression test.
