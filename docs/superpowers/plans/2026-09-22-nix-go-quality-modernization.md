# Nix and Go Quality Modernization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Nix the local/CI toolchain authority, upgrade the language toolchain, enforce Go exhaustiveness checks, and replace string logging with Zap.

**Architecture:** A root flake pins development and build tools while Make remains the command interface. Go analyzers run as separate pinned commands so their policy is visible. A small observability package constructs and injects Zap loggers without exposing Zap to domain packages.

**Tech Stack:** Nix flakes, nixpkgs, Yarn Berry 4, Go 1.27.1, Node 24.21.0, TypeScript 7.0.2, staticcheck, exhaustive, go-check-sumtype, Uber Zap

**Spec:** `docs/PLATFORM_MODERNIZATION_DESIGN.ko.md`

## Global Constraints

- Solid must remain on the Solid 2 release-candidate line; do not downgrade to Solid 1.
- Pin related Solid packages to the same RC release whenever that version exists.
- Make targets remain the human-facing interface and must work as `nix develop --command make <target>`.
- Use `yarn-berry_4`, `fetchYarnBerryDeps`, and `yarnBerryConfigHook`; do not introduce legacy Yarn 1 `yarn2nix`.
- Production logs are JSON and must not expose authorization values, tokens, secrets, or database passwords.
- A task is complete only when its focused tests and the final repository verification pass.

## Review Focus

- Apple Silicon and x86_64 Linux must resolve the same declared versions; Task 1 adds flake evaluation checks on both supported systems.
- Yarn offline builds must fail on lockfile drift rather than reaching the network; Task 1 exercises an immutable offline install.
- New enum constants must break CI until switches/maps are updated; Task 3 adds analyzer fixtures proving this.
- Zap fields containing credentials must be redacted before encoding; Task 4 keeps the existing adversarial redaction cases.
- Logger flush errors for unsupported stdout/stderr sync operations must not turn a clean shutdown into a failure; Task 4 tests both ignored and real errors.

---

### Task 1: Pin the reproducible toolchain

**Files:**
- Create: `flake.nix`
- Create: `flake.lock`
- Create: `nix/yarn-deps.nix`
- Create: `.envrc`
- Modify: `Makefile`
- Modify: `README.md`
- Test: `nix/flake-interface-test.sh`

**Interfaces:**
- Consumes: `package.json`, `yarn.lock`, `apps/api/go.mod`
- Produces: dev shell commands `go`, `node`, `yarn`, `scythe`, `staticcheck`, `exhaustive`, `go-check-sumtype`; Make targets `nix-check` and `tool-versions`

- [ ] **Step 1: Write the flake interface test**

Create `nix/flake-interface-test.sh` with assertions for `go version`, `node --version`, `yarn --version`, and presence of all four Go/SQL tools. Assert exact versions `go1.27.1`, `v24.21.0`, and `4.18.0`.

- [ ] **Step 2: Run the test before the flake exists**

Run: `sh nix/flake-interface-test.sh`

Expected: FAIL because the declared tools are not all available from a project flake.

- [ ] **Step 3: Add the flake and Yarn Berry dependency derivation**

Define `devShells` for `aarch64-darwin`, `x86_64-linux`, and `aarch64-linux`. Use a pinned nixpkgs revision that contains or packages the exact targets. Build Scythe and missing analyzers with fixed source and vendor hashes rather than `@latest`. Define a Yarn dependency derivation using:

```nix
yarnOfflineCache = pkgs.fetchYarnBerryDeps {
  yarnLock = ./yarn.lock;
  hash = pkgs.lib.fakeHash;
};
```

and include `pkgs.yarnBerryConfigHook` in the web build hook chain. The first build must fail with
the computed hash; replace `pkgs.lib.fakeHash` with exactly that reported value.

- [ ] **Step 4: Make Nix evaluation and offline install pass**

Run:

```bash
nix flake check
nix develop --command sh nix/flake-interface-test.sh
nix develop --command yarn install --immutable --immutable-cache
```

Expected: all commands exit 0 without modifying `yarn.lock`.

- [ ] **Step 5: Document the entry point and commit**

Document `nix develop`, optional direnv, and `nix develop --command make check`. Commit the task with only flake, lock, Nix helper, Makefile, `.envrc`, and README changes.

### Task 2: Upgrade runtime and frontend build versions

**Files:**
- Modify: `apps/api/go.mod`
- Modify: `package.json`
- Modify: `apps/web/package.json`
- Modify: `packages/contracts/package.json`
- Modify: `packages/custom-provider-sdk/package.json`
- Modify: `yarn.lock`
- Modify: `apps/web/test/architecture.test.mjs`
- Modify: `apps/api/Dockerfile`
- Modify: `apps/web/Dockerfile`

**Interfaces:**
- Consumes: toolchain from Task 1
- Produces: Go 1.27.1 module and containers; Node 24.21.0/Yarn 4.18.0 workspace; Solid 2 RC-compatible build

- [ ] **Step 1: Change architecture tests to the new exact version contract**

Update `apps/web/test/architecture.test.mjs` to assert Solid 2 RC.9-compatible package pins and the selected matching router/vite-plugin versions. Add assertions for root `packageManager: "yarn@4.18.0"` and TypeScript `7.0.2`.

- [ ] **Step 2: Verify the old manifests fail the new assertions**

Run: `nix develop --command yarn workspace @jandibat/web test`

Expected: FAIL on the old `2.0.0-rc.0`, Yarn 4.6.0, or TypeScript 5.7 pins.

- [ ] **Step 3: Upgrade manifests and regenerate the lockfile**

Set Go to `1.27.1`, Node image/tooling to `24.21.0`, Yarn to `4.18.0`, TypeScript to `7.0.2`, Vite to `8.3.0`, Vitest to `5.0.1`, and Solid packages to the chosen mutually compatible 2.0 RC set. Use exact versions for RC/next packages.

- [ ] **Step 4: Repair compile and test failures caused by major upgrades**

Run focused commands after each repair:

```bash
nix develop --command make typecheck
nix develop --command make test-web test-sdk
nix develop --command make test-api
nix develop --command make build-web
```

Expected: all exit 0 under the new toolchain.

- [ ] **Step 5: Build both production images and commit**

Run the two Docker builds currently used by CI and assert their configured non-root users. Commit manifests, lockfile, compatibility edits, and Dockerfile pins together.

### Task 3: Enforce enum and sum-type exhaustiveness

**Files:**
- Create: `apps/api/internal/staticanalysis/testdata/src/exhaustiveness/exhaustiveness.go`
- Create: `apps/api/internal/staticanalysis/staticanalysis_test.go`
- Modify: `Makefile`
- Modify: enum/sum-type declarations and switches under `apps/api/internal/**`

**Interfaces:**
- Consumes: `staticcheck`, `exhaustive`, `go-check-sumtype` from Task 1
- Produces: Make target `lint-api`; sealed-interface convention using package-private marker methods

- [ ] **Step 1: Add an analyzer policy fixture that is intentionally incomplete**

The fixture must contain one named-constant switch missing a value and one sealed-interface type switch missing a variant. The test invokes both analyzer binaries and asserts that each fixture is reported.

- [ ] **Step 2: Add `lint-api` and observe failures in production code**

Define:

```make
lint-api:
	cd apps/api && go vet ./...
	cd apps/api && staticcheck ./...
	cd apps/api && exhaustive -check=switch,map ./...
	cd apps/api && go-check-sumtype ./...
```

Run: `nix develop --command make lint-api`

Expected: FAIL on currently incomplete switches or unsealed sum types, while the policy test separately confirms the analyzers detect its negative fixtures.

- [ ] **Step 3: Seal intended sum types and make production switches exhaustive**

Add an unexported marker method to closed interfaces and their variants. Add explicit cases for every named enum constant. Do not silence findings with empty defaults; use a documented analyzer ignore only where the input is intentionally open-ended.

- [ ] **Step 4: Run analyzers and unit tests**

Run:

```bash
nix develop --command make lint-api
nix develop --command make test-api
```

Expected: both pass, and adding a temporary variant to a fixture demonstrates the analyzer still fails.

- [ ] **Step 5: Commit the static policy**

Commit the Make target, fixtures, declaration changes, and exhaustive switch changes as one reviewable unit.

### Task 4: Replace string logging with injected Zap

**Files:**
- Modify: `apps/api/internal/observability/logging.go`
- Modify: `apps/api/internal/observability/logging_test.go`
- Create: `apps/api/internal/observability/zap.go`
- Create: `apps/api/internal/observability/zap_test.go`
- Modify: `apps/api/cmd/server/main.go`
- Modify: `apps/api/cmd/server/composition.go`
- Modify: `apps/api/cmd/worker/main.go`
- Modify: `apps/api/cmd/worker/composition.go`
- Modify: `apps/api/cmd/maintenance/main.go`
- Modify: `apps/api/cmd/maintenance/composition.go`
- Modify: call sites found by `rg 'observability\.Logf|log\.' apps/api`
- Modify: `apps/api/go.mod`
- Modify: `apps/api/go.sum`

**Interfaces:**
- Consumes: runtime resource fields `BuildSHA`, `Environment`, `Region`
- Produces: `observability.NewLogger(Config) (*zap.Logger, func() error, error)` and structured event logging

- [ ] **Step 1: Extend logging tests around the desired JSON contract**

Use `zaptest/observer` for field assertions and a JSON buffer for encoder assertions. Cover fixed resource fields, request ID, event validation, newline injection, bearer/basic credentials, query-string secrets, and password-bearing PostgreSQL URLs. Add a sync test where `EINVAL`/`ENOTTY` is ignored but an arbitrary error is returned.

- [ ] **Step 2: Run the focused tests against the old logger**

Run: `nix develop --command sh -c 'cd apps/api && go test ./internal/observability -run "Logger|Redact|Sync"'`

Expected: FAIL because Zap construction/injection does not exist.

- [ ] **Step 3: Implement Zap construction and redacting field helpers**

Build production JSON and development console configurations. Expose typed helpers such as `SafeError(error) zap.Field` and `SafeString(key, value string) zap.Field`; keep the existing redaction regex behavior at the boundary. Bind fixed fields with `logger.With` once.

- [ ] **Step 4: Inject loggers through all three composition roots**

Pass `*zap.Logger` to HTTP middleware, workers, adapters, and operational jobs that log. Domain packages remain logger-free. Delete `Default`/global logging after `rg` proves there are no call sites.

- [ ] **Step 5: Verify logging and process shutdown behavior**

Run:

```bash
nix develop --command sh -c 'cd apps/api && go test ./internal/observability ./internal/processruntime ./cmd/server ./cmd/worker ./cmd/maintenance'
nix develop --command make lint-api test-api
```

Expected: all pass and a process smoke log parses as one JSON object per line.

- [ ] **Step 6: Commit the logging migration**

Commit Zap dependencies, observability tests/implementation, and all migrated call sites together.

### Task 5: Make Nix the CI authority

**Files:**
- Modify: `.github/workflows/ci.yml`
- Modify: `.github/workflows/deploy-staging.yml`
- Modify: `Makefile`
- Modify: `docs/CONFIGURATION.ko.md`

**Interfaces:**
- Consumes: Task 1 flake and Tasks 2–4 verification targets
- Produces: CI command `nix develop --command make ci`; no separate language-version environment variables

- [ ] **Step 1: Add a repository check for duplicate version authorities**

Add a shell assertion that CI has no `GO_VERSION`, `NODE_VERSION`, `YARN_VERSION`, `setup-go`, `setup-node`, or `corepack prepare` entries after migration.

- [ ] **Step 2: Run the assertion and confirm it fails**

Run the new check directly.

Expected: FAIL on the current workflow pins.

- [ ] **Step 3: Install Nix in CI and run Make targets inside the flake**

Use a commit-SHA-pinned Nix installer action, enable the Nix store cache, and replace language setup steps with `nix develop --command make ...`. Add `lint-api` to `check`. Preserve security scans and image builds.

- [ ] **Step 4: Run the complete local equivalent**

Run:

```bash
nix flake check
nix develop --command make ci
```

Expected: all checks pass from a clean checkout with no generated diff.

- [ ] **Step 5: Commit and record evidence**

Commit workflow, Makefile, and configuration documentation changes. Record the successful command set in the PR or execution log rather than committing transient output.
