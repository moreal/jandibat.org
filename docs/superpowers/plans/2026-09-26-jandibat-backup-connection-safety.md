# Jandibat Backup Connection Safety Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete native-backup Task 2 with secret-safe idempotency, minimal SQL
roles and native x86 secure S3 proof, while keeping deployment policy separately owned.

**Architecture:** Root account bootstrap creates a private fixed-name digest view
and insert-only metadata contract. The phase-specific connection command commits
connection plus baseline atomically, verifies both input and catalog digests, then
checks storage and grants USAGE. A narrowly filtered OPS log policy is proven in
the fixture and separately enforced in homelab before any credential-bearing job.

**Tech Stack:** CockroachDB 26.2.5, POSIX shell/Cockroach CLI, Nix-pinned S3Proxy,
Node contract tests, native x86_64 Linux Docker, Helm 21.0.4/Flux manifests.

**Spec:** [Backup connection safety design](../specs/2026-09-26-jandibat-backup-connection-safety-design.md).
Read it with the original homelab native-backup plan Task 2 and Global Constraints.

## Global Constraints

- Existing main in each repository; no extra worktree per user direction. Preserve unrelated changes, `.direnv/`, and stash `59d814c`.
- Coordination owns root scripts, fixture/Nix/CI and these docs in jandibat.org; homelab owns its manifests, rendered-policy tests and operational runbooks in a separate reviewed commit.
- The user's standing approval covers local development, review and CI-only iteration/pushes without repeated approval prompts.
- Real SOPS credentials, B2 configuration/deletion, homelab push/Flux reconciliation, DNS/Cloudflare apply, existing DB/PVC changes and production restore/failover retain separate exact-target approval gates.
- Secure CockroachDB 26.2.5 uses `cockroachdb/cockroach:v26.2.5@sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282`; fixture architecture is native x86_64-linux; Darwin exit 77 is SKIP, never PASS.
- No SQL admin/root fallback, raw catalog access, metadata UPDATE/DELETE, connection ALTER/DROP or automatic adoption/rotation in the connection wrapper.
- All DSNs use `COCKROACH_URL` environment only; SQL uses stdin. No credential/digest/full URI in output, argv, commits, CI artifacts or chat.
- The root bootstrap receives no S3 credential; non-root fixture clients mount no root client key; synthetic authenticated HTTPS S3Proxy is the only storage used for proof.
- Existing homelab callers supply only four app-role passwords: backup-account provisioning is an explicit all-three-input mode. No backup inputs preserves app-only behavior; partial/empty/placeholder input fails before SQL. Do not enable the connection phase until homelab Secret allowlist and isolated DB gate are updated.
- One independently reviewed commit per task in the named repository. Every commit has exactly one `Assisted-by: Codex:<exact-active-model-id>` trailer from the committing agent's identity hook; ask only if that exact identity is unavailable. Preserve other tools' trailers and never use AI Co-authored-by.
- Development/CI fixture proof is not operational promotion; the five-image release receipt and later backup/restore H2 gates remain mandatory.

## Review Focus

- Missing/empty/malformed view results and inherited public privileges must fail closed instead of proving absence or equality (Tasks 1–2).
- A lost COMMIT response, concurrent run or interrupted grant phase must never adopt drift, insert another baseline or announce success prematurely (Tasks 2–3).
- Successful S3 CREATE, failed CREATE and encoded credential characters may log differently; every sink must stay clean while audit events remain present (Task 3).
- A copied but unused logging file, chart-added sink or old DB pod must not satisfy the deployment prerequisite (Task 5).
- Internal catalog/version changes and restored stale metadata must stop bootstrap without widening authority or rewriting fingerprints (Tasks 1–2).

## File map and sequence

| Task | Repository / owner | Deliverable |
| --- | --- | --- |
| 1 | jandibat.org / Coordination | Root-owned metadata/view and exact SQL privilege contract. |
| 2 | jandibat.org / Coordination | Canonical input, fail-closed transactional connection bootstrap and resumable verification. |
| 3 | jandibat.org / Coordination | Native x86 secure S3 and all-sink logging proof; Task 2 GREEN gate. |
| 4 | jandibat.org / Coordination | Reviewed connection scripts/config contract in existing restore-tools payload. |
| 5 | homelab / Coordination | Rendered DB logging prerequisite and promotion policy, without push/apply. |

Execute sequentially because each task consumes the preceding contract; obtain
fresh independent SQL/security review for each. A passing unit test is not native
S3 proof. Intermediate reviewed commits may leave the existing overall Task 2
fixture RED; mark that state explicitly and do not issue a release readiness claim.

### Task 1: Root-owned digest and immutable metadata boundary

**Files (jandibat.org):** Modify `scripts/db-bootstrap-roles.sh`,
`scripts/db-verify-backup-roles.sh`, `scripts/test-db-bootstrap-roles.sh`,
`scripts/backup-fixture-client.sh`, `scripts/test-db-backup-roles-secure.sh`;
modify `scripts/test-db-backup-boundary.sh` for the active verifier's missing-DSN
refusal; create `scripts/test-db-backup-metadata.sh`; update `Makefile`,
`.github/workflows/ci.yml` and
`docs/interface-change-log.md`.

**Interfaces:** Produce the three LOGIN identities and the exact SQL objects,
columns/constraints and grants in the design's Data and role interface.
`connection_live_digest(connection_name STRING, catalog_digest STRING)` exposes
one fixed-name SHA-256 value only. `connection_policy` has fixed name, version 1,
non-null 64-character lowercase input/catalog digests. Root bootstrap validates
existing object definitions/ownership; backup role verification checks effective
as well as direct authority and uses fixed diagnostics only.

- [ ] **RED:** Add `missing_private_schema`, `wrong_owner`, `wrong_view_predicate`,
  `nullable_digest`, `unexpected_public_grant`, `extra_role_membership` and
  `unsafe_internal_session_missing` cases. Also assert no backup inputs retain the
  existing four-role bootstrap behavior and any one/two backup inputs abort before
  SQL; empty/placeholder values abort. Assert malformed existing objects are
  rejected rather than replaced; bootstrap can SELECT/INSERT the fixed record but
  cannot UPDATE/DELETE/CREATE schema objects/read raw catalog or expose other names.
- [ ] **Run RED:** `nix develop --command sh scripts/test-db-backup-metadata.sh`
  and `nix develop --command sh scripts/test-db-bootstrap-roles.sh`; record named
  failing assertions rather than raw SQL/error output.
- [ ] **Implement:** Create roles and exact operational SQL objects in existing
  root bootstrap, with no S3 input. Use schema/table constraints from the spec,
  verify existing objects on rerun and reject public/default privilege leakage.
  Enable `allow_unsafe_internals` only in required SQL sessions. Extend grant
  parsers for the private defaultdb/schema/table/view privileges; do not continue
  assuming the only database grant is runner BACKUP. Add a named Make target for
  metadata tests and include it in `make check`. Also invoke that target explicitly
  in the hosted migration job before the native fixture; CI does not run `make check`
  as a whole, so these fake failure cases need their own persistent gate.
- [ ] **GREEN:** Run both focused scripts, existing bootstrap tests, and native
  fixture metadata assertions. Record raw-catalog and UPDATE/DELETE `42501`
  negatives. Complete `nix develop --command make check` and `git diff --check`.
  Overall secure-S3 fixture may still fail its missing connection-command gate.
- [ ] **Review and commit:** Independent owner/view/default-grant review, then
  stage only these task files and commit `feat: add private backup policy metadata`
  with the exact active-model assistance trailer.

### Task 2: Canonical connection state machine and atomic baseline

**Files (jandibat.org):** Create `scripts/db-bootstrap-backup-connection.sh` and
`scripts/test-db-backup-roles.sh`; modify `scripts/db-verify-backup-roles.sh`,
`scripts/test-db-backup-metadata.sh` as needed for connection postconditions;
modify `Makefile` and `.github/workflows/ci.yml` for persistent CI wiring.
Read saved stash files without popping
or restoring them; reuse only reviewed logic.

**Interfaces:** The command consumes the exact `BACKUP_*` environment names,
canonical URI/digest algorithm and state table from the spec. Exit 0 means identity,
policy equality, all-node CHECK and exact grants verified. Exit 2 means invalid
input/policy; SQL/transport failure exits 1; missing client exits 127. Output is a
fixed diagnostic, never data. CHECK/GRANT resume is allowed only for matching
committed metadata and expected incomplete grants; no metadata repair.

- [ ] **RED:** Cover `same_input_noop`, each endpoint/region/bucket/prefix/path-style
  and credential mutation, reserved UTF-8 query bytes, invalid authority/port,
  control characters and malformed/pre-encoded endpoint. Pin digest determinism
  and assert no input value appears in argv/stdout/stderr.
- [ ] **RED:** Cover missing live row, missing baseline, zero-row join, duplicate
  or malformed result, unsupported policy version, changed live digest and wrong
  identity. Assert no CREATE/grant on refusal and no ALTER/DROP/UPSERT/UPDATE ever.
- [ ] **RED:** Inject failure before INSERT and COMMIT, uncertain commit, concurrent
  uniqueness conflict, CHECK empty/failed node/unknown columns, partial grants and
  drift after CHECK. Assert both SQL objects roll back before commit; after commit
  preserve the exact pair and resume only from revalidated equality. No new grant
  on CHECK failure; final grant/digest verification failure never reports success.
- [ ] **Run RED:** `nix develop --command sh scripts/test-db-backup-roles.sh`;
  expected missing command/state-machine assertions. Fakes test orchestration,
  not Cockroach transaction or S3 semantics.
- [ ] **Implement:** Shell helpers validate inputs, encode once and hash stdin;
  one SQL connection creates connection plus INSERT inside BEGIN/COMMIT. Capture
  output privately and strictly parse counts/digests. Abort on transient/uncertain
  SQL failures; a later invocation reclassifies state. After CHECK, reread policy,
  grant both USAGE privileges in one transaction and verify final postconditions.
- [ ] **GREEN:** Run role/metadata/bootstrap/boundary tests, ShellCheck through
  `nix develop --command make check`, and `git diff --check`. Verify both new
  named Make targets are dependencies of `check` and the CI migration job invokes
  the role test before the secure fixture. Verify the tests
  reject both raw and percent-encoded synthetic sentinels in outward output.
- [ ] **Review and commit:** Independent state-machine and secret-flow review,
  then commit only Task 2 files as `feat: bootstrap backup connections with drift checks`.
  Do not call Task 2 complete before Task 3 native S3 proof.

### Task 3: Preserve audit visibility and prove secure native S3

**Files (jandibat.org):** Create `scripts/fixtures/cockroach-backup-logging.yaml`;
modify `scripts/test-db-backup-roles-secure.sh`, `scripts/backup-fixture-client.sh`,
`scripts/test-db-backup-roles-secure-fixture.test.mjs`, `.github/workflows/ci.yml`
only if additional gate wiring is required. Keep `flake.nix` pinned tools unchanged
unless the proof identifies an actual missing dependency.

**Interfaces:** Fixture consumes Tasks 1–2 scripts and the exact server-log YAML
from the spec using a read-only mount plus `--log-config-file`. Receipt emits only
phase/result booleans, SQLSTATEs and revisions. Production accepts no fixture
inventory. Keep the existing scanner over all DB logs plus container and storage
logs on every exit; missing/unreadable logs or failed cleanup is failure.

- [ ] **RED:** Add contract tests rejecting a missing config mount/argument, global
  WARNING, disabled audit channels, an OPS INFO route in any sink, weakened sentinel
  scan and a success path that skips CHECK or native architecture verification.
- [ ] **RED native characterization:** In a separate disposable node with default
  logs, exercise synthetic CREATE and assert the private scanner detects the access
  ID without printing it. Confirm redaction-only still fails. Report only booleans;
  erase private artifacts after checking cleanup. This expected negative must not
  weaken the final fixture's unconditional no-sentinel requirement.
- [ ] **Implement:** Use the complete tested YAML; validate effective config with
  pinned `cockroach debug check-log-config`, mount it before server startup and
  keep stray-error capture. Do not start the disposable DB with Docker `--rm`:
  stop it first, wait for terminal state, copy all final logs and container output,
  scan them, and only then remove it. Interrupted runs use the same quiesce,
  collect and scan sequence or fail closed if it cannot finish. Enable fixture
  SQL statement logging as root and
  generate positive CREATE SQL/security/denied-sensitive-read audit events.
- [ ] **GREEN native functional proof:** Run
  `nix develop .#backup-fixture --command sh scripts/test-db-backup-roles-secure.sh`
  on native x86_64 Linux. Require real authenticated HTTPS S3 CREATE+CHECK under
  bootstrap, same-input rerun, unchanged count/baseline, malformed/changed input,
  out-of-band ALTER, explicit rollback, orphan records, phase interruption and
  concurrent attempts. Prove exactly one committed pair and no unauthorized grant.
  Show the stored digest changes for ALTER without showing its value.
  At least one successful authenticated S3 variant uses a synthetic credential
  requiring percent encoding, beyond the existing unreserved-only fixture values.
- [ ] **GREEN native log proof:** Scan all sinks for raw/encoded access ID, secret,
  DSN passwords and complete URI on successful CREATE, failed CREATE/CHECK and
  malformed input. Require zero matches plus actual SQL/security/sensitive audit
  markers. Fail if a sink is absent. Keep S3Proxy output private and checked too.
- [ ] **Privilege decision:** Start without EXTERNALIOIMPLICITACCESS. If custom S3
  fails with a proven privilege denial, record sanitized SQLSTATE and review the
  contemplated bootstrap-only grant before adding exact positive/negative tests.
  A network/TLS error is not privilege evidence. Any wider authority stops work.
- [ ] **GREEN local:**
  `nix develop --command node --test scripts/test-db-backup-roles-secure-fixture.test.mjs`,
  `nix develop --command make check`, and `git diff --check`. Native CI is required;
  Darwin SKIP and the previous arm64 nodelocal/timeout probes cannot substitute.
- [ ] **Review and commit:** Independent SQL/storage/logging review of successful
  native evidence; commit `test: prove secure backup bootstrap and log boundaries`.
  Update ignored H2 ledger with sanitized evidence; never commit raw log artifacts.

### Task 4: Package only the reviewed scripts and publish the interface

**Files (jandibat.org):** Modify `scripts/build-release-images.sh`,
`scripts/test-restore-tools-payload.sh`, `scripts/test-image-release-policy.mjs`,
`docs/IMAGE_RUNTIME_CONTRACT.ko.md`, `docs/interface-change-log.md`.

**Interfaces:** Existing restore-tools image includes executable
`/workspace/scripts/db-bootstrap-backup-connection.sh` and
`/workspace/scripts/db-verify-backup-roles.sh` alongside existing account/migration
scripts. It does not gain root certs, S3 credentials, fixture logs or a sixth image.
Runtime docs identify the DB logging prerequisite and phase env names, never values.

- [ ] **RED:** Require both exact script paths/hashes/executable modes in archive
  and image; reject either missing/tampered file or fixture/credential inclusion.
  In a disposable non-root, read-only restore-tools container, exercise the wrapper
  with only private writable scratch and synthetic phase input; assert required
  encoding/hash tools are present and missing input fails without an output leak.
- [ ] **Run RED:** `nix develop --command node --test scripts/test-image-release-policy.mjs`;
  expected named payload mismatch before allowlist updates.
- [ ] **Implement:** Extend exact payload allowlists/copy loop and interface docs
  from the reviewed Task 1–3 result. Do not blindly apply the saved stash.
- [ ] **GREEN:** Run release policy and restore-tools payload checks against the
  built image, `nix develop --command make check`, generated-artifact drift check,
  and `git diff --check`; complete required Linux five-image build/smoke/scan receipt
  before any deployable promotion. Preserve truthful incomplete receipt status.
- [ ] **Review and commit:** Independent packaging review, commit
  `build: package verified backup connection scripts`. No operational publish/apply.

### Task 5: Homelab logging prerequisite and promotion policy

**Files (homelab only):** Modify
`services/jandibat/k8s/cockroach-helmrelease.yaml`,
`services/jandibat/k8s/tests/cockroach-values.sh`,
`services/jandibat/k8s/tests/rollout-gates.sh`,
`services/jandibat/k8s/tests/secret-boundaries.sh`,
`services/jandibat/k8s/tests/isolated-db-gates.sh`,
`docs/runbooks/jandibat-backup-restore.md`;
create `services/jandibat/k8s/tests/backup-logging-policy.sh` and
`docs/evidence/jandibat-backup/TASK2-CONNECTION-TEMPLATE.md`.
Use the pinned chart's supported `conf.log` value to embed the reviewed nonsecret
YAML if the rendered command proves this mapping; if that mapping is unsupported,
inspect the pinned chart's supported config-file mount/argument wiring and revise
this task's exact file list before implementation. This routine wiring choice
does not require another development approval.

**Interfaces:** Rendered secure single-node DB starts with the Task 3 effective
logging policy and a pod-template revision changed by policy changes. The connection
phase cannot be promoted before the reviewed DB rollout/prerequisite is recorded.
Update the bootstrap Secret key allowlist and isolated DB gate for the all-three
backup-account mode before emitting a connection phase; retain the existing
four-role app-only path until the reviewed backup-account Secret skeleton is
introduced under the original H2 Task 8. No incomplete placeholder may enter
an active backup-account Job.
This task adds no runnable connection Job with real credentials; later original
native-backup rollout tasks consume the same prerequisite. Parent ownership of
stable Secrets and HelmRelease and revisioned phase-Job ownership stay intact.

- [ ] **RED:** Mutate the rendered policy to include OPS INFO, omit audit channels,
  add an unfiltered sink, retain a stale pod-template revision or leave the config
  unused. Require each mutation to fail `backup-logging-policy.sh`; assert TLS,
  retained PVC and exact image pin are unchanged. Test that root/S3 Secret mounts
  are still limited to their separate authorized phases. The existing four-role
  isolated DB gate stays green, while a synthetic backup-account mode with only
  one/two of the three password keys fails before SQL or promotion.
- [ ] **Run RED:** `nix develop --command sh services/jandibat/k8s/tests/backup-logging-policy.sh`;
  expect the current chart values' missing explicit log policy to fail.
- [ ] **Implement:** Embed and render the reviewed policy through exact chart
  21.0.4; compare normalized channel/threshold/audit semantics with Task 3 proof.
  Validate the effective rendered config with the pinned Cockroach binary. Preserve
  any stricter existing redaction only with equivalent sentinel/audit proof.
  Document OPS INFO diagnostic loss, OPS warning collection, effective SQL audit
  settings, restart impact and rollback that cannot re-enable leaky CREATE retries.
- [ ] **GREEN:** Run the new test, cockroach-values/render/secret-boundaries/
  rollout-gates tests and relevant homelab required checks (`just check`,
  `just tofu-check`, `just tofu-test` via `nix develop --command`), plus
  `git diff --check`. Retain pinned chart archive verification and inspect the
  actual startup command/config, not just source-file patterns.
- [ ] **Review and commit locally:** Independent homelab manifest/promotion review;
  commit `fix: gate backup bootstrap on safe Cockroach logging` in homelab only.
  No homelab push, Flux reconcile, credential entry or real DB restart in this task.

## Completion and later operational gate

Task 2 development completion requires the reviewed native x86 S3+grant+drift+
all-sink evidence, reviewed payload and separate homelab policy. If successful S3,
private-schema semantics or logging errors remain unproven, record the precise
remaining gate; do not issue a partial GREEN claim. Before any separately approved
operation, present exact DB/PVC inventory, manifest/image revisions, bucket/prefix
identity and lock policy, role/Secret key names, dependency graph, outage/rollback
plan and targets. No credential values appear in that review.

Original schedule/chain/verifier/retention/restore tasks follow only after their
own proof. Connection CHECK alone is not a full backup, a verified chain or a
measured RPO/RTO outcome. Development approval already exists; this plan adds no
repetitive development or design approval prompt.
