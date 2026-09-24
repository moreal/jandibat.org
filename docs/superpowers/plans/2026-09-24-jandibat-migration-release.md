# Jandibat Migration Payload and Release Evidence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish an immutable, secret-safe five-image release whose restore-tools image can bootstrap database roles, migrate schema, grant runtime privileges, and verify those privileges before homelab starts the app.

**Architecture:** Keep four separate Nix application images and extend the pinned Cockroach-based restore-tools packaging with only versioned SQL and scripts. Make Cockroach CLI consumers use `COCKROACH_URL` instead of password-bearing argv. Publish one same-source-SHA release receipt and five digest-scanned images through a publish-only workflow that cannot deploy staging or production.

**Tech Stack:** Nix flake, Docker Buildx, CockroachDB 26.2.5, shell, Node test runner, GitHub Actions, Skopeo, Syft, Grype

**Spec:** [`../specs/2026-09-24-jandibat-migration-gates-design.md`](../specs/2026-09-24-jandibat-migration-gates-design.md)

## Global Constraints

- Use the current `main`; do not create a branch or worktree. Preserve the untracked `.direnv/` and all other user changes.
- Make one independently reviewed commit per Task, with exactly one `Assisted-by: Codex:gpt-6-sol` trailer. Use unsigned commits as already authorized.
- Run local commands through `nix develop --command make <target>` where a Make target exists; Linux image archive/runtime claims require the x86_64 Linux CI builder.
- Never print a password, DSN with credentials, token, certificate private key, or production secret in chat, logs, Git diff, an image, or an artifact.
- No Cockroach database/PVC deletion, DNS/Cloudflare apply, Flux production reconcile, actual SOPS credential input, B2 bucket/Object Lock/retention change, or production restore/failover without separate exact-target plan and approval.
- The four Nix workload images remain separate; `restore-tools` stays the fifth scanned release image. Static GraphQL SDL and OpenAPI HTTP-edge contracts are unaffected.
- SQLSTATE 40001 retry remains at the Go transaction boundary; do not add retry behavior to migration SQL generation.

## Review Focus

- A malicious or malformed role password must be rejected before SQL, not quoted into a statement; Task 3 runs both mock and real-DB rejection tests.
- A credential-bearing URL must never appear in the `cockroach sql` child argv or process logs; Task 2 traces exact argv while keeping secret bytes out of test output.
- A migration SQL or script edit must change the packaged image and be detectable in the archive; Task 1 checks source hashes and a missing-file mutation.
- A failed first publication, partial scan, or mixed source SHA must not export deploy refs; Task 4 tests missing, mismatched, and partially scanned receipts. A fully verified first publication does export its five digest refs.
- A publish-only run must never invoke a staging or production deployment; Task 5 parses the workflow and checks its job graph.

---

### Task 1: Package the immutable database payload in restore-tools

**Files:**
- Modify: `deploy/restore-tools.Dockerfile`
- Modify: `scripts/build-release-images.sh`
- Create: `scripts/test-restore-tools-payload.sh`
- Modify: `scripts/test-image-release-policy.mjs`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `db/migrations/*.sql`, `scripts/db-migrate-url.sh`, `scripts/db-configure-runtime-roles.sh`, and `scripts/db-verify-runtime-roles.sh`. Task 3 adds its bootstrap script to the same inventory.
- Produces: read-only `/workspace/db/migrations` and `/workspace/scripts/<name>.sh` inside `restore-tools`, with Cockroach CLI and shell tools; one archive receipt whose hash changes with any consumed file.

- [ ] **Step 1: Write a failing packaging policy test.** In `scripts/test-image-release-policy.mjs`, assert `build-release-images.sh` copies only the three existing named scripts and the migration directory into the temporary Docker build context, `restore-tools.Dockerfile` copies those paths into `/workspace`, and `test-restore-tools-payload.sh` is called after archive import. Add a mutation fixture that omits `0001_baseline.sql` and must fail.

  ```js
  const dockerfile = readFileSync(join(root, 'deploy/restore-tools.Dockerfile'), 'utf8');
  assert.match(dockerfile, /^COPY db\/migrations\/ \/workspace\/db\/migrations\/$/m);
  assert.match(dockerfile, /^COPY scripts\/ \/workspace\/scripts\/$/m);
  assert.match(readFileSync(script('build-release-images.sh'), 'utf8'), /test-restore-tools-payload\.sh/);
  ```
- [ ] **Step 2: Observe RED.** Run `nix develop --command make image-release-policy-test`; expect failure because the Docker context currently contains only OCI image layouts and no migration files.
- [ ] **Step 3: Add the minimal packaging code.** Copy exact files into the temporary context before `docker buildx build`; use `COPY db/migrations/ /workspace/db/migrations/` and `COPY scripts/ /workspace/scripts/` in the Dockerfile. Keep the existing pinned Cockroach base and Nix API/maintenance contexts. The payload test should compare `sha256sum` values from the checked-in sources with values read from the imported archive and require executable script modes and CLI presence. Execute a process smoke with `docker run --rm --user 65532:65532 --read-only --tmpfs /tmp --entrypoint /bin/sh "$image_id" -c 'test -w /tmp && /cockroach/cockroach version >/dev/null'` to prove the intended Kubernetes non-root `/tmp` contract; `image_id` is the imported archive's recorded `sha256:` ID.

  ```sh
  mkdir -p "$restore_context/db/migrations" "$restore_context/scripts"
  cp db/migrations/*.sql "$restore_context/db/migrations/"
  for file in db-migrate-url.sh db-configure-runtime-roles.sh db-verify-runtime-roles.sh; do
    cp "scripts/$file" "$restore_context/scripts/$file"
  done
  ```
- [ ] **Step 4: Verify GREEN.** Run `nix develop --command make image-release-policy-test image-runtime-contract-test`; run `nix develop --command make check`. Linux CI later runs `nix develop .#images --command sh scripts/build-release-images.sh "$RUNNER_TEMP/image-evidence"`; on Darwin record the explicit archive/runtime skip, not a pass.
- [ ] **Step 5: Independent review and commit.** Review image contents for credentials, unexpected source paths, and digest drift; run `git diff --check`, then commit only Task 1 files with the required trailer.

### Task 2: Remove credential-bearing Cockroach CLI argv

**Files:**
- Modify: `scripts/db-migrate-url.sh`
- Modify: `scripts/db-configure-runtime-roles.sh`
- Modify: `scripts/db-verify-runtime-roles.sh`
- Create: `scripts/test-db-cli-secret-boundary.sh`
- Modify: `Makefile`
- Modify: `docs/IMAGE_RUNTIME_CONTRACT.ko.md`

**Interfaces:**
- Consumes: existing `MIGRATION_DATABASE_URL`, `DATABASE_URL` test fallback, and three role-verification URLs.
- Produces: unchanged SQL behavior, but every `cockroach sql` child receives the URL only as `COCKROACH_URL` in its environment; no `--url` argument.

- [ ] **Step 1: Write a failing child-process test.** Put a fake `cockroach` executable first in `PATH`; have it reject any argument matching `--url*`, assert `COCKROACH_URL` is present, and return fixed TSV fixtures for `SELECT current_database()`, `SHOW USERS`, and schema checks. Run the real scripts with synthetic URLs containing a canary password; test output may contain only `redacted` and must not print the canary. Cover the positive and missing-URL paths for all three scripts.

  ```sh
  for arg in "$@"; do case "$arg" in --url*) echo 'secret URL passed in argv' >&2; exit 91;; esac; done
  test -n "${COCKROACH_URL:-}" || exit 92
  ```
- [ ] **Step 2: Observe RED.** Run `nix develop --command sh scripts/test-db-cli-secret-boundary.sh`; expect rejection of current `--url="$database_url"` calls.
- [ ] **Step 3: Replace argument delivery only.** Use `COCKROACH_URL="$url" "$sql_bin" sql --set=errexit=true ...` in each script's SQL adapter; preserve database-name preflight, migration checksum behavior, grants, and role negative checks. The pinned Cockroach 26.2.5 CLI advertises `COCKROACH_URL` as the `--url` environment equivalent.

  ```sh
  sql() {
    COCKROACH_URL="$database_url" "$sql_bin" sql --set=errexit=true "$@"
  }
  ```
- [ ] **Step 4: Verify GREEN.** Run `nix develop --command sh scripts/test-db-cli-secret-boundary.sh`, `nix develop --command make migration-atomicity-test db-runtime-roles-test`, and `nix develop --command make check`; inspect process traces without printing env values.
- [ ] **Step 5: Independent review and commit.** Review each CLI invocation (including failure/relock paths), `git diff --check`, and commit Task 2 files only.

### Task 3: Idempotent account bootstrap without password disclosure

**Files:**
- Create: `scripts/db-bootstrap-roles.sh`
- Create: `scripts/test-db-bootstrap-roles.sh`
- Create: `scripts/test-db-bootstrap-roles-secure.sh`
- Modify: `scripts/build-release-images.sh`
- Modify: `scripts/test-restore-tools-payload.sh`
- Modify: `Makefile`
- Modify: `docs/runbooks/DATABASE_ROLES.ko.md`
- Modify: `scripts/test-image-release-policy.mjs`

**Interfaces:**
- Consumes: `COCKROACH_ROOT_URL` with chart-generated root client cert, `JANDIBAT_{MIGRATOR,API,WORKER,MAINTENANCE}_PASSWORD`, and corresponding role DSNs supplied by distinct SOPS keys in homelab.
- Produces: database `jandibat`, four LOGIN users with independent passwords, and successful login verification; no schema grants or deletion.

- [ ] **Step 1: Write failing tests.** In `test-db-bootstrap-roles.sh`, use a fixed canary consisting of 43 `A` characters plus 43 `B` characters for two roles; reject empty/duplicate passwords, a quote/newline/semicolon, fewer than 43 unpadded base64url characters, a missing root URL, and `--url` argv. Force the fake SQL client to fail on account creation while echoing its stdin statement to stderr; assert the bootstrap wrapper returns nonzero and neither stdout nor stderr contains either canary. In `test-db-bootstrap-roles-secure.sh`, require a real secure 26.2.5 cluster: run bootstrap twice and verify one database/four LOGIN users, then rotate one synthetic password and prove the new credential works while the old one fails. The existing `make db-up` cluster is `--insecure`, so it must never be used for password-rejection evidence.
- [ ] **Step 2: Observe RED.** Run `nix develop --command make db-bootstrap-roles-test`; expect missing script/target failure. Run `nix develop --command sh scripts/test-db-bootstrap-roles-secure.sh` on a Linux Docker host; expect missing bootstrap failure. Never point either script at a production URL.
- [ ] **Step 3: Implement the smallest bootstrap.** Validate all inputs before connecting. Use the fixed four role identifiers and this rule for every password: `case "$password" in ''|*[!A-Za-z0-9_-]*) exit 2;; esac; test "${#password}" -ge 43 || exit 2`; reject duplicates and `REPLACE_ME`. Use `COCKROACH_URL="$COCKROACH_ROOT_URL" "$sql_bin" sql --set=errexit=true` with SQL on stdin, never `--execute` or a URL argv. Run `CREATE DATABASE IF NOT EXISTS jandibat`, fixed-name `CREATE USER IF NOT EXISTS`, then controlled `ALTER USER ... WITH PASSWORD` with only the validated alphabet. Capture CLI stdout/stderr privately with `umask 077`, emit only a fixed error code/message on failure, and remove the captured files on every exit. Validate each user with its own role DSN via `COCKROACH_URL` after creation. A legacy/unmanaged schema must not be dropped or altered.
- [ ] **Step 4: Build the secure fixture.** In `test-db-bootstrap-roles-secure.sh`, use the pinned `cockroachdb/cockroach:v26.2.5@sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282` image, a private `mktemp -d`, and `cockroach cert create-ca`, `create-node localhost 127.0.0.1`, `create-client root` to start a disposable `start-single-node --certs-dir` container. Copy only the bootstrap script into the temporary context; run the pinned client image with `--network container:<isolated-db>`, a private `--env-file` containing synthetic credentials/DSNs, and read-only cert/script mounts. Never mount the repository root or pass passwords in `docker run` argv. Stop only the container created by the test and remove its private directory on exit.

  ```sh
  fixture_dir=$(mktemp -d)
  crdb_image='cockroachdb/cockroach:v26.2.5@sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282'
  mkdir -p "$fixture_dir/certs"
  docker run --rm --mount "type=bind,src=$fixture_dir/certs,dst=/certs" --entrypoint /cockroach/cockroach "$crdb_image" cert create-ca --certs-dir=/certs --ca-key=/certs/ca.key
  docker run --rm --mount "type=bind,src=$fixture_dir/certs,dst=/certs" --entrypoint /cockroach/cockroach "$crdb_image" cert create-node localhost 127.0.0.1 --certs-dir=/certs --ca-key=/certs/ca.key
  docker run --rm --mount "type=bind,src=$fixture_dir/certs,dst=/certs" --entrypoint /cockroach/cockroach "$crdb_image" cert create-client root --certs-dir=/certs --ca-key=/certs/ca.key
  ```

  The test then starts one named container from that image with `start-single-node --certs-dir=/certs`, places only synthetic passwords and DSNs in a mode-0600 env file, and launches a client with `docker run --env-file "$fixture_dir/roles.env" --network "container:$fixture_name"` and read-only mounts of the copied script and cert directory. Its trap stops `"$fixture_name"` and deletes only `"$fixture_dir"`.
- [ ] **Step 5: Verify GREEN.** Run `nix develop --command make db-bootstrap-roles-test db-runtime-roles-test migration-atomicity-test`, followed by `nix develop --command make check`. On x86_64 Linux run `nix develop --command sh scripts/test-db-bootstrap-roles-secure.sh` and require wrong-password rejection; Darwin reports an explicit SKIP, never a pass. Add the bootstrap script to Task 1's exact build-context copy and archive hash inventory, then rerun `nix develop --command make image-release-policy-test`.
- [ ] **Step 6: Independent review and commit.** Review SQL injection handling, forced CLI-error redaction, secure-cluster password rejection, and no-secret output; run `git diff --check`, then commit Task 3 files only.

### Task 4: Bind release metadata to five scanned digests and one source SHA

**Files:**
- Modify: `scripts/image-release.mjs`
- Modify: `scripts/test-image-release-policy.mjs`
- Modify: `docs/IMAGE_RUNTIME_CONTRACT.ko.md`
- Create: `docs/IMAGE_RELEASE_EVIDENCE_CONTRACT.ko.md`

**Interfaces:**
- Consumes: existing five archive receipts, registry inspection, `GITHUB_SHA`, and successful Syft/Grype results.
- Produces: portable aggregate `release.json` containing `sourceSha`, five `{name, tag, ref, imageId, manifestDigest, scanReceipt, scanReceiptSha256}` entries; each per-image receipt contains artifact-relative SBOM/Syft/Grype basenames and SHA-256 file hashes tied to its image ID and registry digest.

- [ ] **Step 1: Write failing receipt fixtures.** Assert the aggregate has exactly `api`, `worker`, `maintenance`, `web`, `restore-tools`; each `tag` ends with the same 40-hex `sourceSha`, each `ref` is `@sha256:<64 hex>`, each scan receipt matches its image ID and registry digest, and no output is exported when any tag, digest, scan, or receipt is missing or mismatched. Copy a fixture evidence directory to a different temporary root and validate again; any absolute runner path must fail. Mutate Skopeo's post-publish tag response to a different digest and require failure before `GITHUB_OUTPUT` is written.

  ```js
  assert.deepEqual(receipt.images.map(image => image.name).sort(), ['api','maintenance','restore-tools','web','worker']);
  for (const image of receipt.images) {
    assert.equal(image.tag.split(':').at(-1), receipt.sourceSha);
    assert.match(image.ref, /@sha256:[0-9a-f]{64}$/);
    assert.equal(image.scanReceipt, basename(image.scanReceipt));
  }
  ```
- [ ] **Step 2: Observe RED.** Run `nix develop --command make image-release-policy-test`; expect missing `sourceSha`, `tag`, and `scanReceipt` fields.
- [ ] **Step 3: Add portable metadata after the existing all-five scan gate.** Implement this aggregate shape (one entry per name), with `scanReceipt` a basename rather than `$RUNNER_TEMP` path:

  ```json
  {"schemaVersion":1,"sourceSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","images":[{"name":"api","tag":"ghcr.io/moreal/jandibat.org/api:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ref":"ghcr.io/moreal/jandibat.org/api@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","imageId":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","manifestDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","scanReceipt":"api-sha256-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.release.json","scanReceiptSha256":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}]}
  ```

  A per-image receipt contains `imageId`, `manifestDigest`, and `artifacts.spdx`, `artifacts.syft`, `artifacts.grype`; each artifact has exactly `file` (the basename, for example `api-sha256-bbbb.spdx.json`) and `sha256` (64 lowercase hex characters computed from that file's bytes). Preserve fail-closed Skopeo absence parsing and immutable-tag preflight. After each copy and registry scan, resolve both `candidate.ref` **and** `candidate.tag` and require the same `manifestDigest`; only after all five post-publish checks pass may the aggregate and `GITHUB_OUTPUT` be written. Document exact schema and relocation validation in `docs/IMAGE_RELEASE_EVIDENCE_CONTRACT.ko.md`.
- [ ] **Step 4: Verify GREEN.** Run `nix develop --command make image-release-policy-test ci-version-authority-check` and `nix develop --command make check`; move the evidence fixture to another directory and rerun the receipt validator. Inspect fixture outputs for secret canaries and generated artifact drift.
- [ ] **Step 5: Independent review and commit.** Review partial-publication behavior, `git diff --check`, and commit Task 4 files only.

### Task 5: Publish images without dispatching staging

**Files:**
- Create: `.github/workflows/publish-images.yml`
- Modify: `scripts/test-image-release-policy.mjs`
- Modify: `scripts/check-ci-version-authority.sh`
- Modify: `docs/IMAGE_RUNTIME_CONTRACT.ko.md`

**Interfaces:**
- Consumes: Task 4 release receipts and the committed human security-review evidence required by the existing staging security gate.
- Produces: five immutable GHCR `:<full-sha>` tags, five registry digest refs and SBOM/Grype evidence artifacts; no deployment job or credential injection into a runtime host.

- [ ] **Step 1: Write a failing workflow policy test.** Parse the new workflow with the existing `yaml(...)` helper in `test-image-release-policy.mjs` and assert `Object.keys(workflow.jobs).sort()` equals `['publish', 'security-review']`. Ruby's YAML parser used by that helper maps the unquoted `on:` key to JSON key `"true"`, so use `const events = workflow.true` and require `events.workflow_dispatch.inputs.security_review_path.required === true`. Require `workflow.jobs.publish.needs === 'security-review'`, `workflow.jobs.publish.permissions.packages === 'write'`, a read-only checkout (`persist-credentials: false`), pinned Nix/action revisions, `build-release-images.sh`, `image-release.mjs publish`, and artifact upload with `if-no-files-found: error`. Reject any step containing `ssh`, `docker compose`, `kubectl`, or `flux reconcile`; reject an environment/deploy job.
- [ ] **Step 2: Observe RED.** Run `nix develop --command make image-release-policy-test ci-version-authority-check`; expect the new workflow to be absent.
- [ ] **Step 3: Implement publish-only workflow.** Use this job skeleton and the exact pinned action SHAs/validation body already in `deploy-staging.yml`; the publish job performs build, GHCR login, `image-release.mjs publish`, and evidence upload only:

  ```yaml
  on:
    workflow_dispatch:
      inputs:
        security_review_path:
          required: true
          type: string
  permissions:
    contents: read
  jobs:
    security-review:
      runs-on: ubuntu-24.04
    publish:
      needs: security-review
      runs-on: ubuntu-24.04
      permissions:
        contents: read
        packages: write
  ```

  In `security-review`, copy the existing reviewed-SHA ancestry/diff check verbatim so a record cannot authorize later code changes. Keep full source SHA tags and all-five preflight/scan behavior; do not add mutable production tags or any staging deploy/rollback step.
- [ ] **Step 4: Verify GREEN locally.** Run `nix develop --command make image-release-policy-test ci-version-authority-check`, `nix develop --command make check`, and review the workflow job graph. This is not permission to deploy staging or reconcile Flux.
- [ ] **Step 5: Independent review and commit.** Check `git diff --check`, generated artifact drift, and permission scope; commit Task 5 files only.
- [ ] **Step 6: Obtain actual release evidence.** After explicit release authorization and a committed review record, push the workflow, dispatch its publish-only run, inspect all five GHCR digest refs and downloadable evidence, then rerun it to prove immutable-tag idempotency. Record actual Linux archive, runtime, SBOM, and registry outcomes before marking M4 complete.

## Plan completion gate

Run `nix develop --command make check`, `nix develop --command make image-release-policy-test image-runtime-contract-test ci-version-authority-check`, all isolated Cockroach tests named above, and the x86_64 Linux image workflow. Inspect the five registry scan receipts and archive payload hashes. Request whole-plan independent review. Do not claim M4 complete from a Darwin-only Nix evaluation or from a workflow still running.
