# Restore-tools Minimal Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve restore-tools' inherited RPM High findings with an accountable minimal glibc runtime while retaining all five-image release and secure database behavior gates.

**Architecture:** Use the existing digest-pinned Cockroach v26.2.5 image only as a binary/license donor; evaluate separately pinned UBI10 micro as the first final-runtime candidate. Preserve Nix-produced static payloads and COPY-only Docker packaging. Accept a candidate only after real package coverage, unchanged Grype policy, and secure numeric non-root runtime proof.

**Tech Stack:** Nix flake, Docker Buildx/BuildKit, CockroachDB 26.2.5, UBI10 micro candidate, shell, Node test runner, Skopeo, Syft, Grype.

**Spec:** [Restore-tools package-accountable minimal runtime design](../specs/2026-09-26-restore-tools-minimal-runtime-design.md).

## Global Constraints

- Work in the existing `main` checkout, preserving `.direnv/` and unrelated changes; the user previously waived a new branch/worktree.
- The user's standing approval “아직 개발 단계이므로 모두 승인합니다. 다시 묻지 마세요.” covers local development/review and CI-only pushes. No repeat approval gate for those actions.
- Exclude GHCR publication/deployment, actual SOPS credential input, B2 configuration/deletion, homelab push or Flux reconciliation, DNS changes, existing database/PVC deletion, retention deletion, and production restore/failover.
- Keep exactly five images and the portable receipt schema; no scan-policy relaxation or hidden package metadata. Preserve `grype --fail-on high` and zero High/Critical acceptance.
- Use `nix develop --command make <target>` for existing host targets and `nix develop .#images --command ...` for image/scanner tools. Add any needed tool to the pinned Nix shell rather than installing it ad hoc in CI.
- Keep Dockerfile `RUN`/`ADD` forbidden. No application compilation outside Nix and no copied Nix store/proxy in restore-tools.
- Never emit raw secrets, credential URLs, private keys, or fixture env contents. Use test-owned isolated resources; cleanup only those resources.
- Each implementation commit is independently reviewed and has exactly one `Assisted-by: Codex:<exact-active-model-id>` trailer supplied by the model-identity hook. Do not reuse the historical plan's model ID; if unavailable, ask before committing. Preserve other tools' trailers.

## Review Focus

- A zero-match scan with an empty RPM catalog must fail package-accountability validation (Task 1).
- A default root image must fail even when an explicit `--user` smoke would pass (Task 2).
- Version output can pass while DNS, native-library loading, or verified TLS fails; require real packaged-client SQL and TLS negatives (Task 3).
- Packaged payload substitutions, missing applets, or writable files must fail before publication (Task 2).
- A fifth-image scan/runtime failure must leave release outputs unavailable; old four-image successes cannot authorize a new release (Task 4).

## File ownership and evidence flow

Platform/Coordination owns these changes; do not modify `apps/api/**`, `apps/web/**`, GraphQL SDL, or OpenAPI. No domain/HTTP contract change is needed. The runtime contract documents the packaging change. Keep new runtime evidence separate from existing `release.json` and per-image scan-receipt schemas.

| File | Responsibility |
| --- | --- |
| `deploy/restore-tools.Dockerfile` | Pinned donor/final base, explicit copies, final user/entrypoint/path |
| `scripts/build-release-images.sh` | Existing Nix extraction/context/builder lifecycle; exact runtime assembly and proof calls |
| `scripts/test-restore-tools-payload.sh` | Exact payload, modes, final config, dependency/runtime and package-accountability checks |
| `scripts/test-restore-tools-secure.sh` (new) | Isolated TLS server and candidate-image client proof; sanitized results |
| `scripts/test-image-release-policy.mjs` | Host regression/mutation tests and proof wiring |
| `scripts/image-release.mjs` | Pre-publication and relocated-evidence sidecar validation before any registry effect |
| `scripts/probe-restore-runtime-candidate.sh`, `.github/workflows/restore-runtime-probe.yml` | Separate CI-only x86 candidate characterization and sanitized artifact; no publish/deploy |
| `scripts/check-ci-version-authority.sh` | Existing Nix/COPY-only boundary, kept intact |
| `flake.nix` (only if required) | Pinned host inspection tools; Coordination-only edit |
| `docs/IMAGE_RUNTIME_CONTRACT.ko.md` | Final image/runtime and coverage contract |

### Task 1: Establish an accountable candidate and failing acceptance tests

**Interfaces:** Consume the exact failed-run artifacts and pinned Cockroach donor. Produce a sanitized candidate dossier with resolved amd64 runtime digest, RPM inventory, loader/native dependency inventory, license paths, proposed copy/app-let allowlists, and either `candidate` or `rejected`. No guessed image digest enters the Dockerfile.

- [ ] Read the spec, original M4 plan, and ignored ledger. Preserve the exact failed-run/image identities and separate the unrelated backup fixture RED status.
- [ ] Add focused host tests for the candidate probe itself: pinned tool use, read-only registry inspection, Linux/amd64 identity, no credential/publish/deploy path, sanitized artifact, and rejection of empty inventory or hidden RPM metadata. Run them RED before the script and GREEN after it. Do not commit final-Dockerfile assertions yet: those belong to Task 2 and would make this preflight-only commit fail its local policy suite before a candidate digest exists.
- [ ] RED-test `scripts/probe-restore-runtime-candidate.sh` and the CI-only `.github/workflows/restore-runtime-probe.yml`: reject mutable-only final references, missing amd64 child digest, absent package/loader/license evidence, a zero-match scan with empty RPM inventory, any High/Critical match, unexpected secrets/permissions or any publish/deploy step. The workflow takes no credential input and uploads only sanitized evidence. Add a pinned inspection tool to Nix only if absent.
- [ ] Implement the separate probe through the Linux-only `nix develop .#images` shell. After focused/full local tests and independent review, commit and CI-only push this Task 1 probe; dispatch only this read-only workflow on the exact source SHA. On actual x86_64 Linux, resolve `registry.access.redhat.com/ubi10/ubi-micro` with flake-locked Skopeo, record candidate digest and platform, scan the base with flake-locked Syft/Grype, and inspect the donor's ELF interpreter/native requirements. Use read-only extraction/inspection; do not run unknown extracted binaries on the host. Do not wait for the existing main CI backup-role RED to turn green before reading this probe's artifact.
- [ ] Record the complete base RPM inventory and packages owning the loader/glibc, with Syft identification and OS metadata. Record original rpmdb paths/hashes for preservation checks. Inventory the donor's required native libraries and actual license/notice paths; freeze exact copy paths and bytes. Wait for the reviewed backup Task 1 handoff before inventorying commands in `db-bootstrap-roles.sh`, then freeze BusyBox applets against that exact source SHA; refresh the inventory whenever later backup Task 4 changes packaged scripts. Record license obligations for later publication review without claiming they are satisfied merely by copying a file.
- [ ] Add probe-level negative package-accountability fixtures: empty RPM catalog, missing loader owner, absent rpmdb and mismatched source digest. The Task 1 probe rejects each despite a synthetic zero-match Grype result and its focused suite is GREEN before commit. Task 2 separately repeats these mutations against the imported final image and its payload validator.
- [ ] If the base itself has High/Critical findings or cannot account for required libraries/trust material, mark this candidate rejected and follow the spec's fallback sequence. Preserve the failing release gate. A clean base scan permits assembly, not acceptance of the final image. Record the workflow run and candidate/rejected result in the ignored M4 ledger; Task 2 consumes only an accepted preflight artifact.

### Task 2: Assemble the candidate and prove payload/default runtime boundaries

**Interfaces:** Consume Task 1's frozen digest and allowlists plus exact scanned API/maintenance image IDs. Preserve `test-restore-tools-payload.sh IMAGE_ID` for existing callers; add an optional second `EVIDENCE_DIR` argument for bound runtime evidence, required in `build-release-images.sh`. Produce the imported candidate ID, existing scan receipt, and `restore-tools-runtime.json` with source SHA, image ID, donor/base refs and resolved manifests, copied-file hashes, package coverage summary, license inventory, and referenced SBOM file/hash. This is supplemental evidence, not a change to the release receipt schema.

- [ ] Extend host fixtures before implementation to reject default user `''`, `0`, or a named user; executable symlink substitution; missing/extra SQL or script; payload mode other than `0555`/`0444`; unexpected writable directories; broken applet links; inherited `/nix/store` or metrics proxy. Preserve existing malformed image-ID and missing baseline tests. Run `nix develop --command make image-release-policy-test` and record RED cases.
- [ ] Add the final-Dockerfile structure assertions now: distinct digest-pinned donor/final base, explicit numeric default user, no full Cockroach final base, and unchanged `RUN`/`ADD` ban and High threshold. Verify RED against the current Dockerfile before implementing the candidate; Task 1 did not commit these transition-failing assertions.
- [ ] Update `deploy/restore-tools.Dockerfile` with Task 1's actual pinned donor/final runtime, exact vendor-native/license copies, explicit `USER 65532:65532`, `/cockroach/cockroach` entrypoint, `CMD ["version"]`, working directory, PATH, and verified CA bundle path. Preserve final base package database/OS metadata/licenses. Use no package installation or OS-library copying from the donor.
- [ ] Update `scripts/build-release-images.sh` to create only the frozen BusyBox applet symlinks in its task-owned context and pass them through COPY; preserve binary dereference, context modes, pinned BuildKit, owned-builder cleanup, timestamp normalization, and archive import. Handle any base-owned path collision explicitly by rejecting the candidate rather than silently overwriting a packaged file.
- [ ] Extend `scripts/test-restore-tools-payload.sh` to inspect config and exact files/modes under the imported image ID, verify Task 1 package coverage/rpmdb preservation, run both default-user and explicit-user read-only CLI/shell checks with dropped capabilities/no-new-privileges, and prove a controlled temporary write fails without `/tmp` and succeeds with tmpfs. Keep existing hashes and absence checks. Bind supplemental evidence to the existing Syft output and exact image/source identity; do not dump environment/config secrets.
- [ ] Run `nix develop --command make image-release-policy-test ci-version-authority-check image-runtime-contract-test`; expect GREEN. On Linux run `nix develop .#images --command sh scripts/build-release-images.sh "$RUNNER_TEMP/image-evidence"`; expect exact-image scan plus new payload checks to pass. Darwin records Linux proof as not run.
- [ ] Review source/provenance/license mapping, inspect `git diff --check`, and commit only Task 2 implementation and tests after an independent review. No image publication.

### Task 3: Prove packaged CLI behavior against verified TLS

**Interfaces:** Create `scripts/test-restore-tools-secure.sh IMAGE_ID EVIDENCE_DIR`. Accept only a full lowercase `sha256:` image ID; execute the candidate's own `/workspace` payload. Produce `restore-tools-secure.json` with source SHA, image ID, server donor digest, and fixed boolean/phase results for verified connection, DNS, bootstrap/migration reruns, grants/roles, rotation, wrong CA, and wrong hostname. Never include URLs or raw SQL/error output.

- [ ] Add host policy/fixture tests proving the secure script uses the passed immutable image ID for client invocations, `/bin/sh` entrypoint, Docker `-i`, default numeric user verification, read-only root, dropped capabilities, no-new-privileges, writable `/tmp`, readonly credential mounts, `sslmode=verify-full`, and the packaged scripts. Fail fixtures for wrong CA, wrong hostname, and a false-positive SQL command that never consumes stdin. Add cleanup ownership and no-secret-output assertions. Observe RED with `nix develop --command make image-release-policy-test`.
- [ ] Implement the isolated fixture using the existing secure bootstrap test as a reference, not as proof for the new client image. Create a unique private network/server with hostname in the certificate SAN; expose no host port and use synthetic credentials only. Generate fixture certificates in a private directory and grant UID/GID 65532 just the read access needed. Preserve the numeric-client test; do not fix permissions by running it as root.
- [ ] Run readiness with a bounded timeout and SQL result assertion. Run packaged bootstrap twice, migrations twice, runtime GRANT and role verification, then rotate a synthetic password and require old-password rejection. Run wrong-CA and wrong-hostname cases against the reachable server and assert actual TLS rejection; absence of connectivity is not TLS validation evidence. Keep credentials in private env files and `COCKROACH_URL`, and emit fixed redacted failure markers.
- [ ] Wire the secure proof after the existing import/scan/payload proof in `scripts/build-release-images.sh`. On failure return nonzero; upload only the sanitized result and ordinary scan evidence. Extend the fake builder lifecycle fixture to recognize the new proof command without weakening separate secure-proof tests.
- [ ] Run `nix develop --command make image-release-policy-test db-bootstrap-roles-test`; run the secure proof on the exact imported image ID via `nix develop .#images --command sh scripts/test-restore-tools-secure.sh "$image_id" "$evidence"` on x86_64 Linux. Require all positive and negative cases to pass and no fixture resource/secret artifact leftovers.
- [ ] Independently review runtime/secret/cleanup boundaries, run `git diff --check`, and commit scoped files with the required current-model trailer.

### Task 4: Validate five-image continuity and document actual evidence

**Interfaces:** Consume Tasks 2–3's sanitized image-bound evidence and existing five-image receipts. Preserve `release.json` schema version 1 and all current publication prerequisites. Produce an updated runtime contract and exact-source CI outcome; registry/publication proof remains pending unless separately authorized.

- [ ] Extend existing policy fixtures so a restore-tools High finding, missing runtime evidence, or failed secure proof stops the build workflow before publish/export. Preserve tests for partial registry scans, mismatched source/image identity, rehashed High findings, tag races, and relocated receipts. Directly invoke `image-release.mjs publish` against candidate layout files with absent, failed, stale-source, wrong-image and wrong-SBOM sidecars; assert zero registry copy/tag inspection/output. `validate` of relocated evidence rejects the same cases. Observe RED before adding validation.
- [ ] Implement only necessary fail-closed validation/wiring in `scripts/image-release.mjs` as well as the build path: sidecar/source/image/SBOM checks occur before publish's first registry action and during relocated `validate`. Keep Syft and `grype --fail-on high` against each exact image ID and, in future separately authorized publication, every registry digest. Preserve the existing five-name aggregate and no-output-before-all-five behavior. Do not broaden `release.json` or existing scan-receipt schemas to absorb supplemental evidence.
- [ ] Update `docs/IMAGE_RUNTIME_CONTRACT.ko.md` with actual adopted base/donor, numeric default user, exact shell/payload inventory, secure smoke, package-accountability requirement, copied-binary SBOM coverage limitations, and separately authorized publication boundary. Explain any rejected UBI candidate and evidence-based fallback.
- [ ] Reconcile with the backup-connection plan before changing shared `scripts/build-release-images.sh`, `scripts/test-restore-tools-payload.sh`, or `scripts/test-image-release-policy.mjs`: preserve the image-side proof and later add the two reviewed backup scripts from that plan in a separate commit. Refresh frozen applet and payload hashes after that handoff; do not let either task overwrite the other's tests.
- [ ] Run `nix develop --command make image-release-policy-test image-runtime-contract-test ci-version-authority-check`, then `nix develop --command make check`, and `git diff --check`. Review generated drift and unrelated changes. Commit reviewed scoped changes with the required trailer.
- [ ] Under standing CI-only development approval, push the reviewed source (separate from Task 1's probe push) and inspect the new x86_64 Linux run. Verify all five local receipts, each exact image ID, zero High/Critical findings, candidate package inventory, and secure/runtime artifacts; verify source/SBOM hash bindings. Record run URL, full SHA, scanner/database metadata, result, and remaining gates in the ignored M4 ledger.
- [ ] Report actual status without declaring M4 complete if backup fixture or publication/idempotency evidence is pending. Do not dispatch publish/deploy, introduce a security-review record on behalf of a human, or use production credentials.

## Stop and fallback conditions

An unresolved High/Critical finding, absent RPM metadata, unaccounted native dependency, license/provenance gap, failed TLS negative, or runtime evidence mismatch keeps release blocked. Diagnose the exact condition, evaluate the next supported pinned candidate under the same tests, and record why it is different. Never strip rpmdb, weaken Grype, copy only the dynamic binary into scratch, or declare a mock/Darwin check to be a Linux result.

## Completion evidence

The focused repair is complete only when code review and local checks pass and a fresh exact-source Linux CI run proves all five local scans plus the candidate's secure runtime. The original M4 release completion gate still separately requires its remaining database work and authorized five-image GHCR publication/immutable rerun evidence. This plan is a document, not evidence that any of those commands have run.
