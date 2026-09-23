# CockroachDB Baseline and Scythe Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the disposable CockroachDB schema history with one baseline and move static SQL adapters to Scythe-generated, type-safe pgx code.

**Architecture:** The baseline migration remains the schema authority. Scythe consumes that schema and adapter-owned SQL blocks, generating pgx functions behind existing application ports. A pgx transaction layer owns Cockroach retry semantics independently of query generation.

**Tech Stack:** CockroachDB 26.2, pgx/v5, Scythe CockroachDB engine and Go pgx backend, Nix

**Spec:** `docs/PLATFORM_MODERNIZATION_DESIGN.ko.md`

## Global Constraints

- Existing CockroachDB data and migration history may be destroyed; do not build data-copy compatibility code.
- The single `0001_baseline.sql` is the canonical initial schema that replaced the old 13-file history. After that reset is verified, new additive migrations may advance live development databases without rewriting the already-applied baseline; migration-history tests must distinguish these later versions from the discarded legacy history.
- Destructive database/PVC removal still requires an explicit operator confirmation at deployment time.
- Test the official unmodified Scythe build first; add a repository patch only for a minimized, reproducible defect.
- Schema/query generated files must be deterministic and checked for drift in CI.
- SQLSTATE `40001` retry belongs to the transaction boundary, not generated queries.
- Never retry external provider, WebAuthn, SMTP, or other network side effects inside a database transaction.

## Review Focus

- Empty and populated fresh databases must converge to the same baseline schema; Task 1 compares catalog fingerprints.
- Cockroach-only `STRING`, `BYTES`, arrays, JSONB and casts must infer correct Go types; Task 2 probes all of them.
- Cancellation during a retry loop must stop immediately rather than consume all attempts; Task 3 has a canceled-context test.
- Dynamic retention/deletion identifiers must reject every non-allowlisted table or column; Task 5 fuzzes the identifier boundary.
- Generated code drift must be caught without a live database, while semantic mismatch is caught with one; Task 6 tests both modes.

---

### Task 1: Replace migration history with a canonical baseline

**Files:**
- Delete: `db/migrations/0001_init.sql` through `db/migrations/0013_mutation_audit_outcomes.sql`
- Create: `db/migrations/0001_baseline.sql`
- Create: `scripts/test-baseline-schema.sh`
- Modify: `scripts/db-migrate.sh`
- Modify: `scripts/db-migrate-url.sh`
- Modify: migration-related tests under `scripts/*.test.sh`
- Modify: `docs/LOCAL_DEV_COCKROACH.ko.md`

**Interfaces:**
- Consumes: current final schema and constraints represented by all 13 migrations
- Produces: one idempotently tracked baseline migration and a stable catalog fingerprint fixture

- [ ] **Step 1: Write a fresh-schema catalog test**

Create a script that starts from an empty `jandibat_baseline_test` database, applies migrations, queries tables/indexes/constraints from `information_schema`/Cockroach catalog views in sorted order, and compares them with a committed expected fingerprint. Apply migrations twice and assert the second run reports the migration as already applied.

- [ ] **Step 2: Run the test against the current history**

Run: `nix develop --command make db-up` followed by `nix develop --command sh scripts/test-baseline-schema.sh`.

Expected: the catalog capture succeeds but the test fails because it expects exactly one migration version.

- [ ] **Step 3: Compose the final schema into `0001_baseline.sql`**

Create tables directly in dependency order. Incorporate the final form of later ALTER/UPDATE migrations without transitional data rewrites. Preserve final indexes, constraints, defaults, runtime roles expectations, and migration checksum tracking.

- [ ] **Step 4: Verify a destructive fresh start**

Remove only the named local Compose Cockroach volume through the repository's documented `db-down`/Compose flow, start a new database, and run:

```bash
nix develop --command make db-up db-migrate db-runtime-roles-test
nix develop --command sh scripts/test-baseline-schema.sh
```

Expected: PASS with one applied migration and the expected catalog fingerprint.

- [ ] **Step 5: Commit the baseline reset**

Commit deleted migrations, new baseline, tests, and the explicit “no upgrade path” documentation together.

### Task 2: Prove official Scythe against CockroachDB

**Files:**
- Create: `scythe.toml`
- Create: `apps/api/internal/adapters/scytheprobe/queries/probe.sql`
- Create: `apps/api/internal/adapters/scytheprobe/probe_integration_test.go`
- Create: `apps/api/internal/adapters/scytheprobe/generated/`
- Modify: `Makefile`
- Modify: `.gitignore`

**Interfaces:**
- Consumes: pinned official Scythe binary from the Nix plan and `db/migrations/0001_baseline.sql`
- Produces: Make targets `sql-generate`, `sql-check`, `sql-check-live`; evidence deciding whether a patch is needed

- [ ] **Step 1: Add representative typed probe queries**

Cover `STRING`, `UUID`, nullable `TIMESTAMPTZ`, `BYTES`, `JSONB`, `STRING[]`, `ANY($1)`, `UPSERT`, `RETURNING`, aggregates, and a transaction-compatible write. Name every SQL block and give nullable inputs explicit casts where inference would be ambiguous.

- [ ] **Step 2: Configure the official CockroachDB/Go pgx backend**

Point Scythe at the baseline schema and probe query directory. Generate into the probe package. Do not apply a patch or PostgreSQL schema rewrite in this step.

- [ ] **Step 3: Run offline generation/check**

Run:

```bash
nix develop --command make sql-generate
nix develop --command make sql-check
git diff --exit-code -- apps/api/internal/adapters/scytheprobe/generated
```

Expected: deterministic generated Go that compiles.

- [ ] **Step 4: Run live Cockroach verification and integration tests**

Run:

```bash
nix develop --command make db-up db-migrate
SCYTHE_DATABASE_URL="$JANDIBAT_TEST_DATABASE_URL" nix develop --command make sql-check-live
nix develop --command sh -c 'cd apps/api && go test ./internal/adapters/scytheprobe -tags=integration'
```

Expected: parameter/result types match Cockroach Parse/Describe behavior and CRUD assertions pass.

- [ ] **Step 5: Record the gate result and commit**

If the probe passes, record that no local patch is present. If it fails, stop migration work, minimize one failing SQL fixture, and proceed to Task 7 before changing production adapters.

### Task 3: Introduce pgxpool and Cockroach transaction retries

**Files:**
- Modify: `apps/api/internal/processruntime/database.go`
- Modify: `apps/api/internal/processruntime/database_test.go`
- Replace: `apps/api/internal/database/transaction.go`
- Modify: `apps/api/internal/adapters/internal/fakedb/fakedb.go`
- Modify: composition roots under `apps/api/cmd/{server,worker,maintenance}/`
- Modify: `apps/api/go.mod`
- Modify: `apps/api/go.sum`

**Interfaces:**
- Consumes: pgx/v5 and generated-query executor requirements
- Produces: `database.DBTX`; `database.InTx(ctx, pool, options, func(context.Context, pgx.Tx) error) error`; retry classification for SQLSTATE 40001

- [ ] **Step 1: Write transaction contract tests**

Test successful commit, callback error rollback, serialization failure followed by success, retry exhaustion, context cancellation, nested transaction reuse, and rejection of a transaction from another pool. Assert the callback may run more than once and document that it must contain DB work only.

- [ ] **Step 2: Run the tests against the database/sql implementation**

Run: `nix develop --command sh -c 'cd apps/api && go test ./internal/database ./internal/processruntime'`

Expected: FAIL because the pgx interfaces do not exist.

- [ ] **Step 3: Implement the minimal pgx transaction boundary**

Define the executor interface from the actual generated Scythe signatures. Use bounded exponential backoff with jitter, context-aware waits, and Cockroach SQLSTATE classification. Preserve lazy transaction behavior only where audited request semantics require it.

- [ ] **Step 4: Switch composition roots to `pgxpool.Pool`**

Use distinct runtime database URLs/roles for API, worker, and maintenance. Configure pool health checks and close pools during shutdown. Keep database handles out of domain packages.

- [ ] **Step 5: Run focused and race tests**

Run:

```bash
nix develop --command sh -c 'cd apps/api && go test -race ./internal/database ./internal/processruntime ./cmd/...'
```

Expected: PASS with no leaked transactions or goroutines.

- [ ] **Step 6: Commit the connection/transaction migration**

Commit the pgxpool boundary separately from adapter query conversion so reviewers can verify retry semantics in isolation.

### Task 4: Convert static adapters to generated queries

**Files:**
- Create: query directories under each `apps/api/internal/adapters/*/cockroach/queries/`
- Create: generated packages under each corresponding `generated/`
- Modify: Cockroach adapter `.go` files under auth, subjects, storage, integrations, operations, ratelimit, and OAuth state
- Modify: existing adapter unit/integration tests
- Delete: static SQL constants moved into `.sql` files

**Interfaces:**
- Consumes: `database.DBTX`, Scythe-generated parameter/result types
- Produces: existing domain/application port implementations with no contract change

- [ ] **Step 1: Choose one read/write vertical slice and add failing adapter tests**

Start with subjects: load by ID/handle, list, insert, update, and delete. Keep port-level expected results unchanged and add null/empty/list-boundary cases.

- [ ] **Step 2: Extract named SQL blocks and generate code**

Move only the selected slice's static SQL to its query file, run `make sql-generate`, and adapt rows to domain models explicitly.

- [ ] **Step 3: Run the slice tests and live verification**

Run the subjects unit and Cockroach integration packages plus `make sql-check-live`.

Expected: PASS and no handwritten SQL constant remains in the slice.

- [ ] **Step 4: Repeat by independently reviewable adapter group**

Convert in this order: auth, activity storage, integrations/OAuth, rate limiting, operations. After each group run its unit/integration tests and commit. Do not combine all adapters into one commit.

- [ ] **Step 5: Confirm static SQL coverage**

Run `rg 'SELECT |INSERT |UPDATE |DELETE |UPSERT ' apps/api/internal/adapters -g '*.go'` and classify every remaining match in a checked-in allowlist comment as dynamic identifier SQL, test fixture, or false positive.

### Task 5: Constrain unavoidable dynamic SQL

**Files:**
- Modify: `apps/api/internal/adapters/operations/cockroach/retention.go`
- Modify: dynamic list/filter adapters identified by Task 4
- Create or modify: corresponding `_test.go` and fuzz tests

**Interfaces:**
- Consumes: generated finite query variants and pgx executor
- Produces: no user-derived SQL identifiers; allowlisted internal identifiers only

- [ ] **Step 1: Add fuzz/property tests for identifier rejection**

Feed quotes, comments, whitespace, Unicode confusables, semicolons, schema-qualified names, empty values, and every non-allowlisted enum to retention/deletion query selection. Assert none reaches an executor.

- [ ] **Step 2: Convert dynamic values to typed parameters**

Replace variable `IN` fragments with array parameters, optional predicates with nullable parameters or finite variants, and sort choices with explicit enum-to-query mappings.

- [ ] **Step 3: Keep identifier variation behind a closed mapping**

For retention tables/columns, map a sealed dataset enum to a compile-time struct of known identifiers. Never interpolate a request string.

- [ ] **Step 4: Run unit, fuzz smoke, and integration tests**

Run the operation adapter tests, a bounded fuzz run, all adapter integration tests, and `make sql-check-live`.

Expected: PASS; the remaining generated SQL matches only the closed mapping.

- [ ] **Step 5: Commit dynamic-query hardening**

Commit implementation and adversarial tests together.

### Task 6: Enforce generated drift in CI

**Files:**
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`
- Modify: `docs/QUERY_BUILDER_DECISION.ko.md`
- Modify: `apps/api/README.md`

**Interfaces:**
- Consumes: all Scythe query directories and generated packages
- Produces: offline PR drift gate and live Cockroach integration gate

- [ ] **Step 1: Add a test that mutates a copied SQL fixture**

In a temporary directory change one selected column and assert `scythe check` exits non-zero without a database. Separately point live check at a schema missing one column and assert semantic verification fails.

- [ ] **Step 2: Add CI stages**

Run `sql-check` in ordinary backend CI. Run `sql-check-live` after a fresh baseline migration in integration CI. Never print the database URL.

- [ ] **Step 3: Remove the probe package if production coverage supersedes it**

Keep compact type fixtures only if they protect Cockroach-specific inference not exercised elsewhere. Otherwise delete the probe package and retain its test cases in production query suites.

- [ ] **Step 4: Run final database verification**

Run:

```bash
nix develop --command make db-up db-migrate db-runtime-roles-test
nix develop --command make sql-check sql-check-live
nix develop --command make test-api test-api-race test-api-integration lint-api
```

Expected: all pass from a fresh empty database and generated files have no diff.

- [ ] **Step 5: Commit CI and decision documentation**

Update the old query-builder decision from “handwritten SQL” to the implemented Scythe policy and include exact regenerate/check commands.

### Task 7: Patch Scythe only after a proven upstream defect

**Files:**
- Create only if needed: `nix/patches/scythe-cockroach.patch`
- Create only if needed: `nix/fixtures/scythe-cockroach-repro/`
- Modify only if needed: `flake.nix`
- Modify only if needed: `docs/QUERY_BUILDER_DECISION.ko.md`

**Interfaces:**
- Consumes: one minimized failing fixture from Task 2 or later live verification
- Produces: patched `scythe-cockroach` binary with the same CLI and an upstreamable regression test

- [ ] **Step 1: Demonstrate failure on the unmodified pinned binary**

Record the exact command, SQL/schema fixture, expected type, actual type/error, Scythe version, and Cockroach version. The fixture must fail without jandibat application code.

- [ ] **Step 2: Add the fixture as a Nix check**

The check first runs the stock derivation and documents its expected failure, then is changed to require success from the patched derivation.

- [ ] **Step 3: Apply the smallest source patch**

Patch only the parser/catalog/type map or Go emitter responsible for the reproduced defect. Do not vendor or fork the whole repository into this codebase.

- [ ] **Step 4: Run upstream and jandibat coverage**

Run the minimized fixture, Scythe's relevant upstream tests if available in the derivation, all repository SQL checks, and live Cockroach integration tests.

- [ ] **Step 5: Document upstream disposition and commit**

Link an upstream issue or PR, explain removal conditions, and commit the patch, fixture, hash update, and documentation together.
