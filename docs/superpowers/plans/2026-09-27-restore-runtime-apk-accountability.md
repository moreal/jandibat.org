# APK-accountable restore-tools runtime execution plan

> Execute on the existing `main` checkout. No branch/worktree. Use test-first Task slices, independent review, and separate commits with exactly one current-model `Assisted-by` trailer. Local development and CI-only pushes have the user's standing approval; GHCR publication/deployment and operational secrets/resources do not.

**Goal:** Make the fifth immutable image pass the unchanged High/Critical gate without hiding packages, while preserving a verifiable glibc/CA/license/runtime chain and the original five-image release boundary.

**Spec:** [APK-accountable glibc fallback design](../specs/2026-09-27-restore-runtime-apk-accountability-design.md). It supplements the [original minimal-runtime plan](2026-09-26-restore-tools-minimal-runtime.md). For this Chainguard APK candidate, Tasks 1 and 2 below replace that plan's RPM-only Task 1 dossier/negative tests and RPM-only stop condition. Its Tasks 2–4 remain required after a successful preflight, with APK-specific package/ownership/license checks substituted for RPM checks. The UBI micro/full/minimal outcomes and original negative fixtures remain historical regression evidence.

**Candidate:** `cgr.dev/chainguard/glibc-dynamic:latest@sha256:6acf5a19a988abdaf0f3d30247561431a206034e702871442bed66a2c68cc1a2` (observed amd64 child `sha256:1e9870bd8b72e908eff47d5f4ffcdca068b2765fbb22223cc5a68c989902d195`), donor `cockroachdb/cockroach:v26.2.5@sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282`. Linux Nix-pinned inspection must re-confirm both identities. The local unexecuted image export is not acceptance evidence.

## Invariants and ownership

- Coordination owns `scripts/**`, `deploy/**`, `flake.nix`, `.github/**`, and `docs/**` here. Do not modify Backend, Frontend, GraphQL SDL or OpenAPI. Preserve `.direnv/` and unrelated changes.
- Keep the probe workflow read-only and secret-free; no registry write, GHCR publication, staging/prod reconcile, real S3/B2, credentials or production restore. Keep `grype --fail-on high` and reject any High/Critical result, even `not-fixed`.
- Preserve immutable index/amd64-child checks, digest-only Skopeo transport, donor native/ELF/license allowlist, base-owned CA/OS/package metadata, and source-SHA binding. The probe's APK parser runs with Nix-pinned Node, not an ad hoc downloaded tool.
- A technical preflight may emit `license-review-pending`. Neither it nor an assembled local image may be published/validated as a release until an independently reviewed committed exact-bound license-text/notice/source-offer record exists. The agent cannot self-approve that record.
- After every Task, report exact commands/results, commit SHA, remaining work and next risk. At plan end run the complete Nix `make check`, generated-artifact drift, exact-source Linux image/scanner and secure DB proof, and whole-plan independent review; never infer Linux success from Darwin mocks.

## Task 1: Strict APK package database contract (TDD)

**Files:** `scripts/probe-restore-runtime-candidate.mjs`, `scripts/test-restore-runtime-probe.mjs`.

1. Add RED fixtures for missing/empty/oversized `/usr/lib/apk/db/installed`, duplicate package name/version, malformed `P:`, `V:`, `L:`, `F:`/`R:` ownership paths, traversal, missing loader/libc owner, missing/ambiguous license declaration, and an empty Syft `apk` catalog with zero Grype matches. Keep secret-bearing rejection/artifact-atomicity tests.
2. Implement a bounded, strict read-only APK installed-DB parser. Record one database path/hash, complete `{name,version,license}` declarations, and a normalized owned-file map. A directly APK-owned final symlink to an in-root directory may appear in the exact-image inventory, but does not confer ownership on target contents. Canonical ELF bytes must resolve within the extracted base and be directly owned as regular files by an installed package; require owners for the ELF interpreter and all required base libraries. Permit only the exact `/etc/mtab -> /proc/mounts` dangling pseudo-filesystem link as metadata in the unexecuted rootfs, never as an ELF owner. Cross-check the exact APK package name/version set against Syft's `apk` artifacts. Keep scanned package, CA, license, and unchanged `grype --fail-on high` gates unchanged. Rename the dossier to schema 2 with `packageManager: "apk"`, `packageDb`, and license declarations; do not place APK bytes in an `rpmdb` field or allow old schema 1 to pass the new candidate gate. Preserve fixed sanitized rejection stages and unchanged scanner policy.
3. Verify `node --test scripts/test-restore-runtime-probe.mjs`, `node --check` on both files, `git diff --check`, and `nix develop --command make check`. Independently review parser bounds, path containment, Syft equality and license semantics; commit only this Task. Do not dispatch the Linux probe while the source still points to UBI.

## Task 2: Pinned APK candidate preflight on x86_64 Linux

**Files:** same probe/test files; update `.github/workflows/restore-runtime-probe.yml` only if an existing read-only gate cannot carry schema 2. Record result in ignored M4 SDD ledger and an append-only reviewed candidate decision.

1. RED-test that the currently selected UBI source is refused by the new APK gate; require the exact Chainguard index, linux/amd64 child, separate v26.2.5 donor, digest-only raw/copy references, CA bundle, OS release, package DB ownership/license declarations, and donor notices. A fixture with Syft/DB package drift or a High finding must reject despite the right digest.
2. Switch only the probe to the pinned APK runtime and retain the donor. No Dockerfile or release-policy change. Re-run focused and full local checks, independent review, then commit and CI-only fast-forward push after checking remote ancestry. Dispatch the existing read-only probe on that exact source SHA; download only sanitized artifact fields. Record exact index/child, package count, trust/owner/license evidence, scanner database time, blocking match count/fix-state and whether a complete candidate dossier exists.
3. If rejected, keep the release blocked and document precise phase/package/CVE/dependency gap before another candidate decision. If zero High/Critical and all technical checks pass, label it **preflight eligible only** and proceed to Task 3. Neither outcome authorizes publication.

## Task 3: Assemble and verify the fifth image

**Files:** `deploy/restore-tools.Dockerfile`, `scripts/build-release-images.sh`, `scripts/test-restore-tools-payload.sh`, `scripts/test-image-release-policy.mjs`, `docs/IMAGE_RUNTIME_CONTRACT.ko.md`; create the secure proof script from the original plan if still absent.

1. After Task 2 eligibility, RED-test distinct digest-pinned donor/final `FROM`, COPY-only build, exact native/notice/Nix payload and BusyBox applets, absent metrics proxy/Nix store, default numeric `65532:65532`, immutable image-ID binding, preserved APK database/hash and exact Syft/owned-file/license declarations. Test default/explicit user, readonly root, dropped capabilities, no-new-privileges, `/tmp` negative/positive, DNS and loader/CA behavior. Do not silently overwrite base-owned paths.
2. Implement only the reviewed final image assembly; preserve five names and the portable release receipt schema. Run real x86_64 Linux archive/import/scan against all five exact image IDs. Keep full scanner provenance and do not remove or conceal APK metadata.
3. RED→GREEN the original plan's secure Cockroach client fixture: packaged scripts/bootstrap and migration twice, least-privilege GRANT proof, verified TLS positive plus wrong-CA/wrong-hostname/old-password negatives, bounded cleanup, no raw secret output. Independently review and commit Task 3 in scoped slices. A clean base scan does not replace this final-image proof.

## Task 4: Fail-closed license and publication boundary

**Files:** `scripts/image-release.mjs`, `scripts/test-image-release-policy.mjs`, runtime sidecar producer/validator and `docs/IMAGE_RELEASE_EVIDENCE_CONTRACT.ko.md` only where necessary; preserve `release.json` schema.

1. Write RED fixtures that directly call `publish` and relocated `validate` with missing, failed, stale-source, wrong-image or wrong-SBOM runtime and secure-proof sidecars from the original plan, as well as absent, pending, stale-source, wrong-base/donor digest, wrong final image ID, wrong SBOM hash, or fabricated/uncommitted license review. Require zero registry inspection/copy/tag/output in every negative case, including a technically green five-image build.
2. Implement the original exact-bound runtime and secure-proof sidecar checks **and** a separate exact-bound `license-review-pending` state. Both check families run before `publish`'s first registry action and during relocated `validate`; neither substitutes for the other. Permit only a committed, independently reviewed human license-text/notice/source-offer record for all seven exact APK packages and donor obligations to advance the license state; bind it to source SHA, base/donor digests, final image ID and SBOM hash. This plan does not create a passing human review record. Keep the existing security-review gate and immutable-tag checks; do not grant the agent publication authority.
3. Run `nix develop --command make image-release-policy-test image-runtime-contract-test ci-version-authority-check`, then `nix develop --command make check`, generated drift and `git diff --check`. Independently review and commit scoped changes. The actual GHCR publish-only workflow, registry receipts and idempotent rerun require separate exact approval and completed human review. If either is absent, M4 remains partial.

## Completion boundary

The APK fallback is technically eligible only with exact-source Linux preflight, a complete final-image package/ELF/CA/license dossier, zero High/Critical findings for all five exact image IDs, secure packaged-client proofs and local checks. M4 release completion additionally requires the original plan's authorized GHCR publication/registry-digest evidence and human security/license review. H1/H2 and isolated native restore/RPO/RTO remain separate later stages; no production action is implied by this plan.
