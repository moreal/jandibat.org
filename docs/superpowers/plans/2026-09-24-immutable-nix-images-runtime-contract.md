# Immutable Nix Images and Runtime Contract Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build reproducible, non-root API, worker, maintenance, and web images from the pinned Nix toolchain, then bind their runtime and release contracts to tested immutable digests.

**Architecture:** Linux Nix derivations build three separate static Go programs and the Solid client with the existing offline Yarn Berry cache. `dockerTools.buildLayeredImage` packages one process per image; the web image starts pinned Nginx after generating only `/tmp/config.json` and `/tmp/security-headers.conf`. CI scans the same image archives that staging publishes and hands digest references to deployment scripts.

**Tech Stack:** Nix/nixpkgs at `flake.lock`, Go 1.27.1, Node 24.21.0, Yarn 4.18.0, Solid 2.0.0-rc.9, nginxMainline 1.31.6, Docker/OCI, SPDX SBOM, Grype.

**Spec:** `docs/PLATFORM_MODERNIZATION_DESIGN.ko.md` sections 7–9 and `docs/MODERNIZATION_BACKLOG.ko.md` M4; deployment consumer: `../homelab/docs/plan-jandibat-service.md`.

## Global Constraints

- Nix is the only Go/Node/Yarn image-build toolchain; do not compile in production Dockerfiles or CI `docker build` stages.
- The four deployable workloads have separate image outputs and numeric non-root users; do not claim a single physical Talos host is HA.
- Build Linux image archives on Linux; Darwin must evaluate the flake and retain the same declared tool versions.
- Use the existing `fetchYarnBerryDeps` and `yarnBerryConfigHook`, not yarn2nix or networked Yarn install.
- Never bake credentials or environment-specific API URLs into images. Runtime secret values enter through environment variables; web writes only to a mounted writable `/tmp`.
- `/livez` is the DB-independent liveness endpoint; `/readyz` and API `/healthz` check dependencies. API `/metrics` is loopback-only.
- Image tags use the full Git SHA; deployment and rollback use `name@sha256:<64 hex>` references. A repeated SHA tag with a different digest is an error.
- Preserve existing SPDX SBOM and high/critical vulnerability gates, and run them on every exact release image digest before staging deploy.
- No registry push, Flux reconcile, DNS/Cloudflare apply, real SOPS credential, production restore, or existing DB/PVC deletion occurs during local verification.

## Review Focus

- An image built twice from the same source/lock must have identical archive hash and must not depend on `created="now"`; Task 2 measures both builds.
- Starting a production process without its role-specific secret or DB DSN must fail closed without revealing the value; Task 3 runs negative startup smoke.
- A read-only web root with writable `/tmp` must still serve `/config.json`, CSP, and `/healthz`, while a missing writable `/tmp` fails startup; Task 2 tests both.
- API liveness must not restart Pods because Cockroach is unavailable; Task 3 pins `/livez` separately from `/readyz`.
- A previously pushed full-SHA tag must not be silently repointed, and SBOM/scan evidence must refer to the digest actually deployed; Task 4 tests a mismatched digest fixture.

---

### Task 1: Build four release payloads with pinned Nix inputs

**Files:**
- Create: `nix/images.nix` (Go and web payload derivations, no image wrapping yet)
- Create: `nix/tests/release-payload-contract.sh`
- Modify: `flake.nix` (`packages` for Linux, leaving Darwin evaluation intact)
- Modify: `nix/yarn-deps.nix` only if the payload source set needs an additional existing workspace file

**Interfaces:**
- Consumes: `mkToolchain`'s `buildGoModule`, `nodejs`, `yarnBerry`, and `yarnDeps.yarnOfflineCache`.
- Produces: `packages.x86_64-linux.{api,worker,maintenance,web}-payload`; each Go output contains exactly one executable under `bin/`, and web contains `dist/client/index.html` plus hashed assets.

- [ ] **Step 1: Write a failing payload contract test.** In `nix/tests/release-payload-contract.sh`, assert the four `nix eval --raw .#packages.x86_64-linux.<name>-payload.drvPath` lookups succeed, `nix flake check --all-systems --no-build` evaluates, and a built web payload contains `dist/client/index.html`. Also assert `rg 'yarnBerryConfigHook|yarnOfflineCache' nix/images.nix` and fail if the release build script invokes `docker build`, `corepack`, or networked `yarn install`.
- [ ] **Step 2: Run the test to observe RED.** Run `nix develop --command sh nix/tests/release-payload-contract.sh`; expect the first missing package output to fail.
- [ ] **Step 3: Add Linux-only payload derivations.** Import `nix/images.nix` from `flake.nix` for Linux systems only. Make three `buildGoModule` outputs from `./apps/api` with `subPackages = [ "./cmd/server" ]`, `[ "./cmd/worker" ]`, or `[ "./cmd/maintenance" ]`, `CGO_ENABLED = 0`, `-trimpath`, and `-ldflags=-buildid=`. Give the web derivation the same repository source set and `yarnOfflineCache` as `nix/yarn-deps.nix`, run `yarn install --immutable --immutable-cache` through `yarnBerryConfigHook`, and run `yarn workspace @jandibat/web build`. Do not add a second tool-version authority.
- [ ] **Step 4: Pin Go module closure and verify offline behavior.** Start each Go derivation with `vendorHash = pkgs.lib.fakeHash`, run `nix build .#api-payload --no-link`, and replace the temporary fake hash with the exact `got:` hash emitted by Nix. Repeat only if the three package closures differ; the committed file must contain no fake hash. Build all four outputs twice on x86_64 Linux, with network disabled for the second build, and compare their store paths. On Darwin, run `nix flake check --all-systems --no-build` and the existing toolchain interface check.
- [ ] **Step 5: Re-run the payload contract and commit.** Run `nix develop --command sh nix/tests/release-payload-contract.sh`, `nix develop --command make graphql-check typecheck build-web`, and `git diff --check`. Request an independent review, then commit only Task 1 files with the required `Assisted-by` trailer.

### Task 2: Package separate immutable OCI-compatible images

**Files:**
- Modify: `nix/images.nix` (four `dockerTools.buildLayeredImage` outputs)
- Create: `nix/web-start.sh` (generate runtime config, then exec Nginx)
- Create: `nix/nginx.conf` only if the packaged main config cannot safely include `apps/web/nginx.conf` directly
- Create: `scripts/test-image-contract.sh`
- Modify: `flake.nix` (Linux image outputs and image checks)
- Modify: `Makefile` (`images-build`, `images-smoke`)

**Interfaces:**
- Consumes: Task 1 payloads and existing `apps/web/nginx.conf` / `apps/web/docker-entrypoint.d/40-runtime-config.sh`.
- Produces: `packages.x86_64-linux.{api,worker,maintenance,web}-image`, Docker archives that `docker load` can import and `skopeo` can copy to OCI registries; the Go images expose their own process entrypoint, not a bundled API binary.

- [ ] **Step 1: Write image contract tests before outputs exist.** `scripts/test-image-contract.sh` must build and inspect all four archives, require architecture `amd64`, numeric non-root `User`, one expected executable and entrypoint per image, no source tree or secret-looking environment value, and deterministic creation timestamp. For web, run `docker run --read-only --tmpfs /tmp:rw,nosuid,nodev --user 101:101` with `JANDIBAT_API_BASE_URL=https://api.example.test`, then assert `/healthz` is `ok`, `/config.json` contains that URL, and CSP `connect-src` contains only the expected origin. Run without writable `/tmp` and expect fail-closed startup.
- [ ] **Step 2: Confirm RED.** Run `nix develop --command sh scripts/test-image-contract.sh`; expect missing `api-image` before adding the image outputs.
- [ ] **Step 3: Implement deterministic images.** Use `pkgs.dockerTools.buildLayeredImage` with fixed `name`, `tag = "nix"`, no wall-clock `created`, `config.User` set to a numeric non-root UID:GID, and `config.Entrypoint` set to the individual Go binary. Include CA certificates and `/busybox` for the existing staging exec health checks; no compiler, Node runtime, or other Go programs in each process image. For web include pinned `nginxMainline`, BusyBox shell tools, Task 1 static assets, existing Nginx site configuration and runtime-config script; `nix/web-start.sh` must execute the script against `/tmp` before `exec nginx -g 'daemon off;'`. Keep Nginx pid/client-body/cache paths under `/tmp` for read-only root filesystems.
- [ ] **Step 4: Build and smoke on Linux.** Run `nix build .#api-image .#worker-image .#maintenance-image .#web-image --no-link`, then `nix develop --command make images-smoke`. Check `/livez` for the three Go processes and `/readyz` as a distinct dependency probe; never use DB-dependent `/healthz` for liveness. Build the same image inputs a second time and compare archive SHA-256 values. On Darwin evaluate all outputs but do not claim a local Linux image build without a Linux builder.
- [ ] **Step 5: Review and commit.** Run `nix develop --command make check`, `git diff --check`, obtain independent image/runtime review, then commit Task 2 files with the required `Assisted-by` trailer.

### Task 3: Pin runtime, migration, and least-privilege role contracts

**Files:**
- Modify: `docs/CONFIGURATION.ko.md`
- Modify: `docs/runbooks/DATABASE_ROLES.ko.md`
- Create: `docs/IMAGE_RUNTIME_CONTRACT.ko.md`
- Create: `scripts/test-image-runtime-contract.sh`
- Modify: `scripts/db-migrate-url.sh` only if a smoke test proves an image-incompatible assumption
- Modify: `apps/web/test/runtime-config.test.sh` only for the read-only `/tmp` startup case

**Interfaces:**
- Consumes: Task 2 image entrypoints and existing `internal/config`, `internal/processruntime`, `db/migrations`, `scripts/db-configure-runtime-roles.sh`.
- Produces: documented per-process environment/secret allowlist, ports/probes, migration Job inputs, and container smoke checks that the homelab manifests can use without guessing.

- [ ] **Step 1: Write contract tests.** Assert API uses port 8080 and `jandibat_api` DSN, worker 8081 and `jandibat_worker`, maintenance 8082 and `jandibat_maintenance`; none receives the migrator DSN. Test production startup without required signing/encryption/HMAC/OAuth/SMTP values fails without logging them. Check `/livez` returns independently of DB, `/readyz` fails while DB is unavailable, and API `/metrics` is loopback-only. Assert migration runner requires `MIGRATION_DATABASE_URL`, `COCKROACH_DATABASE`, `MIGRATIONS_DIR`, Cockroach CLI and a writable temporary directory, and that role configuration precedes application startup.
- [ ] **Step 2: Observe RED.** Run `nix develop --command sh scripts/test-image-runtime-contract.sh`; expect missing `docs/IMAGE_RUNTIME_CONTRACT.ko.md` or an incorrect HMAC encoding assertion to fail.
- [ ] **Step 3: Write the exact runtime document and correct the discrepancy.** In `docs/IMAGE_RUNTIME_CONTRACT.ko.md`, give an env/secret/port/probe table for API, worker, maintenance, web, and migration Job, including CA/TLS mount path and writable `/tmp` for web. Reuse `docs/CONFIGURATION.ko.md` rather than duplicating its full key list. Correct `DELETED_IDENTITY_HMAC_KEYS` to the parser's unpadded standard base64 and exactly 32 bytes; keep `DELETION_PSEUDONYM_KEY` unpadded base64url. Document that API metrics require an in-Pod loopback scrape sidecar/proxy, not a direct Prometheus Pod scrape.
- [ ] **Step 4: Run negative and positive smoke.** Run `nix develop --command sh scripts/test-image-runtime-contract.sh`, `nix develop --command make db-runtime-roles-test`, `nix develop --command make migration-atomicity-test`, `nix develop --command make test-api-race`, and the web runtime config test. Use only the isolated local test database for migration/role probes; no existing database/PVC deletion or production reconcile.
- [ ] **Step 5: Review and commit.** Request independent review of secret isolation and migration idempotency, run `git diff --check`, then commit Task 3 files with the required `Assisted-by` trailer.

### Task 4: Bind CI and staging releases to Nix image digests and scan evidence

**Files:**
- Modify: `.github/workflows/ci.yml`
- Modify: `.github/workflows/deploy-staging.yml`
- Modify: `deploy/staging/compose.yaml`
- Modify: `deploy/staging/README.md`
- Modify: `deploy/restore-tools.Dockerfile` or replace it with a Nix output if it still assumes three binaries in the API image
- Modify: `scripts/deploy-staging.sh`
- Modify: `scripts/rehearse-staging-rollback.sh`
- Modify: `docs/runbooks/DEPLOY_ROLLBACK_MIGRATION.ko.md`
- Create: `scripts/test-image-release-policy.mjs`
- Modify: `scripts/check-ci-version-authority.sh`
- Modify: `Makefile` (`image-release-policy-test` and final image gates)

**Interfaces:**
- Consumes: Task 2 four Nix-built image archives and Task 3 runtime contract.
- Produces: CI image/SBOM/scan gates and staging references `API_IMAGE`, `WORKER_IMAGE`, `MAINTENANCE_IMAGE`, `WEB_IMAGE` as full `@sha256` refs; restore tooling remains digest-pinned.

- [ ] **Step 1: Write failing release-policy fixtures.** `scripts/test-image-release-policy.mjs` should parse workflow and Compose YAML and reject a Dockerfile production build step, a missing worker/maintenance digest, a mutable non-SHA image tag, an SBOM or Grype scan of a tag different from the released digest, missing scan evidence, and a rerun that repoints an existing SHA tag to a different digest. Assert rollback records and restores all four workload refs. Run the test and observe RED against current CI/staging files.
- [ ] **Step 2: Make CI build and scan exact Nix archives.** On Ubuntu with the pinned Nix installer/cache, run `nix build` for four images, import their archives, inspect user/entrypoint, generate SPDX SBOM and high/critical Grype reports for each imported image ID, and upload image-ID-named evidence. No job should build production code from a Dockerfile or invoke independent Go/Node setup.
- [ ] **Step 3: Make staging publish immutable refs without running it locally.** Replace buildx compilation with import/copy of the four Nix archives to GHCR. Use full `$GITHUB_SHA` tags only as immutable lookup keys; if the tag already exists, compare its digest with the candidate and fail on mismatch. Export only `name@sha256:<64 hex>` refs after registry inspection, then generate SPDX SBOM and high/critical Grype reports against those exact registry digest refs before the deploy job can start. Update Compose, deploy, rollback, and evidence scripts for distinct worker/maintenance images. Adapt the restore-tools image so it no longer copies binaries from the API-only image; keep the Cockroach base pinned and scan its released digest too. Do not dispatch `deploy-staging.yml` during verification.
- [ ] **Step 4: Verify policy and actual images.** Run `nix develop --command node --test scripts/test-image-release-policy.mjs`, `nix develop --command make ci-version-authority-check staging-compose-check images-smoke`, `nix develop --command make check`, and Linux CI dry-run image import/inspect/SBOM/scan on local archives. Inspect generated refs and evidence to ensure each SBOM/scan digest equals the deploy digest; do not push to production registry or apply Flux.
- [ ] **Step 5: Review and commit.** Request independent supply-chain review, ensure `git diff --check` and secret scan pass, then commit Task 4 files with the required `Assisted-by` trailer. Report all M4 verification commands/results, M4 commit hashes, remaining H1/H2 backlog, and Talos/S3 risks before starting homelab manifests.

## Plan-End Verification

- [ ] On x86_64 Linux: build all four image outputs, run read-only container smoke, and compare two archive hashes for reproducibility.
- [ ] On Darwin: `nix flake check --all-systems --no-build` and current toolchain interface check pass; do not misreport Linux runtime validation as performed on Darwin.
- [ ] `nix develop --command make check` and `make images-smoke image-release-policy-test` pass with no generated artifact drift.
- [ ] Four image digests, SPDX SBOMs, high/critical scan reports, and rollback refs are verified against the same local candidate; no registry push or production apply is performed.
