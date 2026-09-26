# Restore-tools package-accountable minimal runtime design

## Purpose and status

Resolve M4's fifth-image Grype failure while preserving the existing migration, restore, and immutable release contracts. This is a focused architectural follow-up to the [M4 migration design](2026-09-24-jandibat-migration-gates-design.md) and [original implementation plan](../plans/2026-09-24-jandibat-migration-release.md). It proposes a digest-pinned Cockroach binary donor and a separately pinned minimal glibc runtime. UBI10 micro is the first candidate, not a proven replacement. No runtime digest or successful scan is asserted by this design.

The user's standing development approval is: “아직 개발 단계이므로 모두 승인합니다. 다시 묻지 마세요.” It covers local development, review, and CI-only pushes; do not ask again for those steps. It does not authorize GHCR publication/deployment, actual SOPS credential input, B2 configuration/deletion, homelab push or Flux reconciliation, DNS changes, existing database/PVC deletion, retention deletion, or production restore/failover. This document and its companion plan do not perform those actions.

## Evidence and problem boundary

CI run [36215677122](https://github.com/moreal/jandibat.org/actions/runs/36215677122), at commit `9453cbf`, failed Grype's High/Critical gate on restore-tools image ID `sha256:f2c409703a63ba2ecb212983f484860b4d707a5ff925ce655dccaded91595704`. The downloaded evidence and parent investigation report 44 High RPM matches spanning 19 CVEs inherited from the Cockroach v26.2.5 UBI10 runtime. The four application images had zero High/Critical matches in that run. Counts describe that scanner/database snapshot, not a permanent security property. The ignored M4 progress ledger records the run and image ID; retain a sanitized package/CVE summary with the next implementation evidence.

The Cockroach CLI is dynamically linked and requires glibc runtime libraries. Copying its binary alone into scratch cannot establish a working replacement. The existing image already avoids the API Nix closure and metrics proxy, so that isolation must remain. The separate expected RED backup-role fixture in the migrations job is outside this fix; passing image checks alone does not complete all M4 work.

## Options and selection

1. **Recommended candidate: pinned donor plus pinned UBI10 micro.** Retain the tested Cockroach CLI version, remove unrelated donor OS contents by using a separate runtime, and preserve the runtime's package metadata. This reduces inherited content only if the actual chosen image has fewer unnecessary packages; it does not promise that glibc or other required packages are free of High findings.
2. **Updated supported full Cockroach image.** Potentially the simplest fallback if the vendor ships an image that passes the unchanged gate. A donor/version change requires explicit compatibility and licensing review and the same secure runtime proof.
3. **Another supported minimal glibc distribution.** Consider only if UBI micro cannot satisfy the gate or dependency closure. Require an accounted package inventory, ABI compatibility, license evidence, and the same tests. Do not create an untracked hand-copied glibc tree as a shortcut.

The official [Red Hat catalog](https://catalog.redhat.com/en/software/containers/ubi10/ubi-micro/66f2b273b37c38851ee372f7) and [RHEL 10 container documentation](https://docs.redhat.com/en/documentation/red_hat_enterprise_linux/10/html/building_running_and_managing_containers/adding-software-to-a-ubi-container) identify `registry.access.redhat.com/ubi10/ubi-micro`. They establish candidate availability, not the packages, digest, scan outcome, or compatibility of the image that will be selected. Resolve and record those from an actual `linux/amd64` manifest during implementation.

Because the repository's Nix `.#images` inspection shell is Linux-only, the
first candidate is characterized by a separately reviewed CI-only x86_64 probe
workflow before any Dockerfile base change. Its read-only script records the
resolved manifest digest, source reference, package ownership, loader/native
dependency and license inventory, and exact-base Syft/Grype results as sanitized
artifacts. That workflow cannot publish, deploy, or accept secrets. A candidate
with blocking High/Critical matches is rejected; a clean base scan is only the
handoff to runtime assembly, not acceptance of the final image. The existing
main CI's separate backup-role RED does not invalidate this probe's own evidence.

## Image composition

- Keep `cockroachdb/cockroach:v26.2.5@sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282` as the initial binary donor. Record both the pinned reference and resolved amd64 child manifest when the reference is an index.
- Use a separately digest-pinned UBI10 micro image as the final stage only after preflight succeeds. No mutable-only `FROM` and no guessed digest.
- Inspect ELF interpreter, `DT_NEEDED`, and any vendor native libraries. Freeze an explicit copy allowlist for `/cockroach/cockroach`, required donor-provided native libraries, and required license/notices. Do not copy the donor root filesystem, RPM database, shell, or unrelated OS utilities into the new runtime.
- Runtime OS libraries, loader, NSS/DNS configuration, trust material, OS identity, package database, and base licenses come from the pinned final base. Preserve its real package records; adding unowned OS libraries or removing metadata fails acceptance. If dependencies are missing, reject this candidate and use the fallback process rather than installing packages from mutable repositories.
- Keep `/jandibat-api` and `/jandibat-maintenance` as dereferenced regular executable files from their exact imported and scanned Nix image IDs, and `/busybox` as the separate static Nix `restore-tools-busybox` output. Do not copy `/nix/store` or `/bin/metrics-proxy`.
- Provide `/bin/sh` and the exact shell applets used by the four packaged scripts through explicit symlinks to `/busybox`, generated in the temporary build context. Inventory command usage first; test the resulting shell execution, not merely the links. Do not overwrite a package-owned executable from the base silently.
- Keep the existing COPY-only packaging rule: `scripts/check-ci-version-authority.sh` continues rejecting all Dockerfile `RUN` and `ADD` instructions. Nix remains the application/toolchain authority; do not compile applications in Docker or fetch unpinned packages/tools in CI.

The final config sets `USER 65532:65532`, `WORKDIR /workspace`, and an explicit `/cockroach/cockroach` entrypoint. The default command is `version`, with SQL/jobs continuing to use explicit arguments or `/bin/sh`. Set `PATH` to include `/cockroach`, `/bin`, and `/usr/bin`; preserve `/etc/ssl/certs/ca-certificates.crt` and `SSL_CERT_FILE` as the runtime contract's public trust path, using a checked base-provided target if an alias is needed. Missing usable trust material rejects the candidate. No credential or environment-specific URL is baked into the image.

## Exact payload and permissions

The application payload remains exactly:

| Path | Source and mode |
| --- | --- |
| `/jandibat-api`, `/jandibat-maintenance`, `/busybox` | Exact source binaries; regular files; `0555` |
| `/workspace/db/migrations/*.sql` | Every checked-in `db/migrations/*.sql`, no extras; byte hashes match; `0444` |
| `/workspace/scripts/db-migrate-url.sh` | Checked-in source, byte hash matches; `0555` |
| `/workspace/scripts/db-configure-runtime-roles.sh` | Checked-in source, byte hash matches; `0555` |
| `/workspace/scripts/db-verify-runtime-roles.sh` | Checked-in source, byte hash matches; `0555` |
| `/workspace/scripts/db-bootstrap-roles.sh` | Checked-in source, byte hash matches; `0555` |
| `/workspace/db/migrations`, `/workspace/scripts` | Root-owned directories; `0555` |

The frozen donor/license and BusyBox applet allowlists are separate from this SQL/script inventory. Verify their bytes/targets as well. Root-owned regular payload files are not writable by UID/GID `65532:65532`; copied binaries have no setuid/setgid bits. A numeric-user override is also tested, but it must not mask an empty or root default image user.

Run with a read-only root filesystem, dropped capabilities, no privilege escalation, and writable `/tmp` tmpfs only. A controlled temporary-file operation must fail without `/tmp` and succeed with it. Do not require all CLI commands, such as `version`, to fail without `/tmp`. Mount database CA/client credentials read-only and ensure the numeric UID can read only the fixture credentials it needs.

## Proof required before candidate acceptance

1. **Package accountability:** Preserve the candidate base's RPM database and OS identity and verify Syft identifies its real RPM packages, including the packages owning the loader and required glibc libraries. Compare against a package inventory from the same base digest. An empty or unrecognized OS package catalog is a failure, even if Grype returns zero matches.
2. **Binary/provenance:** Record donor/base manifest identities, copied-file hashes, ELF dependency inspection, Nix payload store paths/image IDs, license/notice inventory, and architecture. Record tool versions and Grype database metadata/time alongside the scan artifacts. Review redistribution obligations from the actual donor and base licenses before publication; a catalog listing is not a substitute for that review.
3. **Runtime:** On actual x86_64 Linux, run the imported final image ID under its default numeric user and again with `--user 65532:65532`, `--read-only`, `--cap-drop ALL`, `--security-opt no-new-privileges`, and `/tmp` tmpfs. Verify version, shell/checksum tools, exact payload/modes, absent Nix store/proxy, DNS lookup, and complete dynamic loading.
4. **Secure DB behavior:** Use the candidate as the client against a disposable secure pinned Cockroach server with synthetic credentials. Use a private container network and certificate SAN matching the server DNS name. Bootstrap twice; migrate twice; apply grants; verify role positive/negative cases; verify password rotation. Require `sslmode=verify-full`, successful SQL over TLS, and explicit failure for a wrong CA, wrong server hostname, and old password. Forward SQL stdin with Docker `-i`. No host script mount may replace the packaged scripts being proved.
5. **Scan and receipt:** Run the existing Syft/SPDX and `grype --fail-on high` checks against the exact imported image ID. Do not weaken thresholds, ignore inherited findings, use `--only-fixed`, hide RPM metadata, or omit restore-tools. Only a candidate with zero High/Critical matches and all runtime proofs proceeds.

Credential-bearing URLs continue reaching Cockroach only via `COCKROACH_URL`, never argv. Fixture env files and private keys stay in a private scratch directory; do not upload them, raw CLI errors, Docker inspect env contents, or credential-bearing SQL. Emit fixed phase/result markers and sanitized diagnostics. Cleanup only test-owned containers, networks, and directories, including partial-failure paths.

SBOM coverage is not equivalent to all shipped code being understood by the scanner. The donor Cockroach executable/native libraries and statically linked Nix binaries may have incomplete package/dependency identification. Preserve scanner coverage details and copied-file/provenance inventories and assess gaps explicitly; “zero matched High/Critical” must never become “all software is vulnerability-free.” A missing required package identity cannot be explained away as this caveat.

## Release continuity and failure handling

Keep `api`, `worker`, `maintenance`, `web`, and `restore-tools` as exactly five images from one full source SHA. Preserve the [existing portable release receipt schema](../../IMAGE_RELEASE_EVIDENCE_CONTRACT.ko.md), local image-ID scans, registry digest scans, hash binding, immutable-tag checks, and fail-closed output behavior. Runtime provenance is an additional sanitized evidence artifact bound to the final image ID, source SHA, and SBOM hash; it does not replace existing receipts or alter their schema.

CI-only image success establishes local archive/runtime/scan proof. GHCR publication, registry receipts, immutable publication rerun, and deployment remain separately authorized work. Never describe an unperformed registry check as passed. On Darwin, record Linux runtime proof as not run.

Import currently writes per-image candidate/layout evidence before the later
restore-tools payload and TLS proofs. Therefore the separately authorized
`image-release.mjs publish` path must check the image-bound supplemental proof
sidecar before its first tag inspection/copy, and `validate` must recheck it for
relocated evidence. Missing, failed, stale-source, wrong-image or wrong-SBOM
sidecars refuse publication/validation with no registry write or output; the
portable `release.json` schema remains unchanged. A build-script failure alone
is not a sufficient publication gate because a caller could reuse the earlier
candidate layout files.

If UBI micro still has High/Critical findings, lacks accountable package metadata, or fails loading/TLS/runtime behavior, record the package/CVE/fix-state and failure phase, retain the existing release block, and evaluate the next supported candidate. No automatic policy exception or production rollout follows. Any donor version change is a separate compatibility decision documented before implementation, covered by the same existing development approval when it remains local/CI-only. If no supported candidate passes, report the precise blocker and upstream remediation dependency.

## Acceptance boundary

This fix is accepted only after focused negative tests, all existing local checks, independent review, and exact-source x86_64 Linux candidate scans and secure runtime tests pass. M4 remains incomplete until its other pending tests and separately authorized actual publication evidence are satisfied. The implementation plan records evidence rather than checking off unperformed tests.
