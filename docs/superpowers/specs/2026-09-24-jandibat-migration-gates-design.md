# Jandibat Migration and Flux Gates Design

> Status: proposed implementation detail for the approved platform modernization.
> This document does not authorize production reconciliation, credential entry,
> deletion, DNS changes, or backup-provider changes.

## Outcome and boundaries

Deploy a new, secure CockroachDB without starting application workloads before
the database, LOGIN users, schema migrations, grants, and positive/negative role
checks all succeed. The existing `restore-tools` release image becomes the
immutable carrier of migration SQL and operator scripts; no sixth image or
runtime credentials are baked into it. This complements
[`IMAGE_RUNTIME_CONTRACT.ko.md`](../../IMAGE_RUNTIME_CONTRACT.ko.md) and the
[homelab deployment design](../../../../homelab/docs/plan-jandibat-service.md).

The four Nix-built application images remain separate. API, worker, and
maintenance receive only their own database DSN and workload-specific secrets.
Web receives no database credential. The migrator receives no application
process secret. Among the added application and pre-deployment jobs, only the
bootstrap job mounts the chart-generated root client certificate; the chart's
own Cockroach Pod retains its chart-managed certificate mounts. A distinct
verification job must use the role-specific DSNs, not
mount the root certificate or run as an application process.

## Immutable migration payload

The pinned Cockroach-based `restore-tools` image includes read-only copies of
`db/migrations/*.sql`, `scripts/db-bootstrap-roles.sh`,
`scripts/db-migrate-url.sh`,
`scripts/db-configure-runtime-roles.sh`, and
`scripts/db-verify-runtime-roles.sh` under `/workspace`. Its archive contract
checks those exact paths and their source hashes, the Cockroach CLI and shell
utilities, and a writable `/tmp` under a non-root Kubernetes security context.
The image release retains one full source-SHA tag and one registry manifest
digest, with the existing SBOM and high/critical vulnerability gates. Neither
the image, CI artifact, nor registry evidence contains a credential or a
production DSN.

The migration job runs the checked-in SQL and scripts from that image, never a
ConfigMap copy or a homelab Git checkout. A change to any migration, grant, or
verification script therefore changes the payload and must produce a new image
digest before deployment. It uses `MIGRATION_DATABASE_URL`,
`COCKROACH_DATABASE=jandibat`, `MIGRATIONS_DIR=/workspace/db/migrations`, a
read-only CA mount and writable `/tmp`. A rerun with the same migration
checksums succeeds; legacy history or a changed applied checksum fails closed.

## Account bootstrap and secret isolation

The chart's self-signer provisions a root client certificate, not application
LOGIN users. An idempotent bootstrap job uses the chart-generated root client
Secret to create the `jandibat` database and the four existing required LOGIN
users: `jandibat_migrator`, `jandibat_api`, `jandibat_worker`, and
`jandibat_maintenance`. It never drops a database, table, user, or PVC. It
validates that each role can log in with its own supplied credential before
reporting success. The account-creation path must be tested against an isolated
real CockroachDB, including a second run and credential rotation. User-supplied
passwords must not appear in shell argv, stdout/stderr, Git diffs, image layers,
or CI artifacts; the implementation must prove safe parameter handling or a
strictly validated, injection-safe encoding before use. The pinned Cockroach
26.2.5 CLI accepts the `COCKROACH_URL` environment variable. The migration,
grant, and verification scripts must pass password-bearing URLs through that
environment variable rather than `--url=<secret>` argv, and negative tests
inspect the child process argv without printing the URL.

SOPS encrypts distinct Kubernetes Secrets for bootstrap, migrator, API,
worker, maintenance, and the role-verification job. The stable parent service
Kustomization owns and decrypts these Secrets; revisioned child
Kustomizations only reference them and never co-own or prune a stable Secret.
Pod specs reference only the keys their phase needs. Among service Jobs and app
workloads, the root certificate/key is available only to bootstrap. The chart
CA certificate is projected read-only to TLS clients,
whose DSNs require server verification. No workload falls back to `root`,
`sslmode=disable`, or an empty placeholder. Actual credential entry remains a
separate operator action requiring prior approval. If an encrypted Secret is
later moved into a child inventory, that child must declare its own SOPS
decryption reference; the parent's setting is not inherited. Credential
rotation is a separately reviewed operation, not an implicit image release.

## Flux ordering and release identity

The service stays inside the dedicated `jandibat` prune boundary. Its parent
Flux Kustomization waits for `HelmRelease/jandibat-cockroachdb` to become Ready.
Service-owned child Kustomizations then reconcile in this order:

1. `jandibat-bootstrap`: one account/database bootstrap Job.
2. `jandibat-migrate`: migration and grant Job with the migrator credential.
3. `jandibat-role-verify`: positive and negative grant checks using dedicated
   role DSNs.
4. `jandibat-app`: API, worker, maintenance, and web workloads and services.

Each child depends on the previous Ready condition and waits for its Job or
workloads to become healthy. A failed or missing Secret, failed Job, or failed
role check leaves dependent workloads unapplied. The three pre-application
Kustomizations and their Jobs have release-revisioned names derived from the
same full **jandibat.org source Git SHA**, not the homelab manifest commit.
The stable `jandibat-app` Kustomization depends on the
exact new `jandibat-role-verify-<sha>` identity. It cannot accept an old
Kustomization's Ready condition merely because metadata labels changed. The
Job pod templates also contain the full release SHA and pinned restore-tools
digest. A second reconcile of the same SHA is idempotent; a new SHA creates
new Jobs. No force-replacement is enabled on the database StatefulSet or PVC.
Each revisioned child Kustomization owns and prunes only its own Job and
uses an immutable per-release Git path; old and new children never reconcile
the same Job or directory. A promotion commit adds the new release children
and changes the stable app dependency to the new verification child, while
keeping old children in the parent's inventory. Only after the new app is
observed Ready does a separate cleanup commit remove old children and their
paths, allowing parent prune to collect their Jobs. Stable parent-owned
Secrets, HelmRelease, StorageClass, and PVC remain outside that cleanup scope.
A render/inventory test exercises two simultaneous revisions and rejects
shared paths, duplicate Job ownership, early old-child removal, or pruning a
stable Secret before any production reconcile.

A machine-enforced promotion check consumes the one aggregate `release.json`
and five per-image `<name>-<digest>.release.json` receipts from one successful
image build. The release metadata is extended to record the full jandibat.org
source SHA and immutable registry tag for every image. The check compares
each registry digest with its scanned receipt and independently resolves its
`:<source-sha>` tag to that same digest. It rejects a missing image,
mixed release, mutable-only tag, placeholder (`REPLACE_ME`), or digest-less
reference. Only a coordinated update of all image refs and the three Job and
Kustomization names can advance `jandibat-app` to that release. Image
automation may stage candidates but must not directly advance only one
production workload. Reconciliation and homelab push require a separate
explicit production approval.

The Cockroach SQL NetworkPolicy must add exactly the `bootstrap` and
`role-verify` pod components to its existing service-owned 26257 allowlist;
tests compare those labels with rendered Job pod templates. It must not open
SQL or Console to other namespaces or create a NodePort. The parent
Kustomization's HelmRelease health check only establishes parent readiness;
the child `dependsOn` edges, not manifest order within the parent, enforce
bootstrap-to-app sequencing.

## Verification and failure cases

Offline tests render every service path and compare the Flux dependency graph,
revisioned Job identities and pod templates, SOPS references, digest-only image refs, non-root/read-only
security contexts, probes, TLS mounts, and Job commands with the runtime
contract. Mutations that remove a dependency, use an old completed Job,
omit a Secret, change only a Job metadata annotation, or supply a placeholder
or mixed-release image must fail the tests.
The isolated Cockroach test runs bootstrap twice, migration twice, and the
positive/negative role script, then rejects a legacy schema and checksum
drift. Linux CI must build/import the extended restore-tools image, inspect
its actual files, and attach SBOM/vulnerability evidence to its published
digest. A local macOS render is not evidence of the Linux container runtime.

No production database/PVC deletion, DNS or Cloudflare apply, Flux production
reconcile, actual SOPS credential entry, B2 bucket/Object Lock or retention
change, or production restore/failover occurs without a separate exact-target
plan and user approval. The single physical host is not HA. Backup and
isolated restore evidence remain required to complete H2.
