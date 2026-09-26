# Jandibat Backup Connection Safety Design

Status: development design addendum for native-backup H2 Task 2. This document
does not claim Task 2 GREEN or production backup readiness. The user's standing
approval covers local development, review and CI-only iteration/pushes without
repeated approval prompts. Real SOPS credentials, B2 configuration/deletion,
homelab push/Flux reconciliation, DNS/Cloudflare apply, existing DB/PVC changes
and production restore/failover retain separate exact-target approval gates.

## Scope and precedence

This addendum makes the safe-idempotency and logging interfaces concrete for
`homelab/docs/superpowers/plans/2026-09-25-jandibat-native-backup-restore.md`
Task 2 and the backup section of
`homelab/docs/superpowers/specs/2026-09-25-jandibat-observability-backup-design.md`.
Their other constraints remain binding: secure CockroachDB 26.2.5, provider-neutral
S3, five release images, distinct SQL actors, no root fallback, chain-aware 35-day
retention, measured RPO <= 3600 seconds and RTO <= 14400 seconds. This work creates
no schedules, retention executor or restore path and changes no domain API.

Coordination owns root scripts, fixture/Nix/CI and these docs in jandibat.org;
homelab owns its manifests, rendered-policy tests and operational runbooks in
a separate reviewed commit. Backend/frontend ownership is unaffected. Work stays
on existing main; preserve unrelated files, `.direnv/`, and stash `59d814c`.
The stash is reference material, not an approved implementation: it refuses all
existing connections and therefore cannot satisfy idempotent reruns.

## Evidence and remaining proof

The local H2 ledger records native x86 CI run `36214446250` reaching the intended
`missing bootstrap LOGIN role` RED after certificate, storage and DB startup.
That proves the fixture can reach its SQL boundary; it does not prove roles or S3.

The separate disposable secure v26.2.5 probe established the following on arm64:

| Observation | Scope and implication |
| --- | --- |
| Bootstrap raw `system.external_connections` SELECT and SHOW CREATE return `42501`. | Do not grant raw catalog access or obtain a credential-bearing SHOW CREATE result. |
| Root-created fixed-name digest view can be selected by bootstrap. | Root creation and every querying session require `SET allow_unsafe_internals = true`; the setting does not grant raw catalog access. |
| CREATE plus metadata INSERT commits together; explicit rollback leaves both absent. | Proven with nodelocal storage, not successful S3; wrapper interruption/concurrency still need tests. |
| Input digest and live catalog digest identify input changes and root ALTER. | Require both comparisons and exactly one row; an empty join is not success. |
| Bootstrap metadata UPDATE/DELETE return `42501`. | INSERT-only authority supports an immutable baseline; private schema and stronger constraints below still need proof. |
| Default OPS INFO logs expose the S3 access ID; `redact: true` alone still exposes it. | Suppressing raw CLI output is insufficient. |
| OPS WARNING routing retains SQL/security/sensitive-access events without either credential sentinel. | Proven for a synthetic S3 CREATE timeout (`57014`), with no custom network sinks. Success/error variants and native x86 remain unproven. |

The probe table was in `defaultdb.public` and its catalog digest was nullable;
the private schema, NOT NULL checks and fixed-name constraints below are proposed
hardening. The probe had an additional `EXTERNALIOIMPLICITACCESS` grant in later
steps; it does not prove that custom S3 requires that grant. Licensed table-audit
configuration failed with `57000`; no table-audit proof is claimed.

## Data and role interface

Only existing root-cert account bootstrap creates the following operational
objects. It receives SQL account passwords but no S3 credential. Objects are
fully qualified, root-owned and outside the application `public` schema:

The existing four-role application bootstrap remains compatible until the
homelab phase graph is migrated: if all three backup-account password inputs
are absent, it performs only the existing app-role work and does not create or
verify backup metadata. If all three are present, it enables this complete
backup-account contract. Any partial set fails before any SQL statement, and
empty or placeholder values never count as present. The secure backup fixture
supplies all three; a no-input or partial-input run cannot declare Task 2 ready.
The future homelab backup-account rollout must update its encrypted Secret key
allowlist and isolated DB gate before enabling the connection phase. An existing
app-only release is not silently promoted into a backup release.

```sql
CREATE SCHEMA defaultdb.jandibat_backup_admin AUTHORIZATION root;
CREATE TABLE defaultdb.jandibat_backup_admin.connection_policy (
  connection_name STRING PRIMARY KEY
    CHECK (connection_name = 'jandibat_backup_v1'),
  policy_version INT NOT NULL CHECK (policy_version = 1),
  input_digest STRING NOT NULL CHECK (input_digest ~ '^[0-9a-f]{64}$'),
  catalog_digest STRING NOT NULL CHECK (catalog_digest ~ '^[0-9a-f]{64}$')
);
SET allow_unsafe_internals = true;
CREATE VIEW defaultdb.jandibat_backup_admin.connection_live_digest AS
  SELECT connection_name, sha256(connection_details) AS catalog_digest
  FROM system.external_connections
  WHERE connection_name = 'jandibat_backup_v1';
```

This is the proposed production object contract, not an assertion that this full
DDL has passed the fixture. Root bootstrap verifies existing ownership, columns,
constraints, view definition and grants on rerun; it must not silently replace,
adopt or repair an incompatible object using CREATE OR REPLACE/IF NOT EXISTS.
No public grants/default grants may expose this schema, table or view. Root
provisions bootstrap CONNECT on `defaultdb`, USAGE on the private schema,
SELECT+INSERT on the table and SELECT on the view, without grant options.
Bootstrap has no schema CREATE, ownership, UPDATE, DELETE or raw system-table
privileges. Explicitly test effective privileges inherited through `public` and
role membership as well as direct grants.

| Actor | Additional authority |
| --- | --- |
| `jandibat_backup_bootstrap` | LOGIN; SYSTEM EXTERNALCONNECTION without grant option; private metadata privileges above; creator's inherent connection DROP/USAGE with grant options. |
| `jandibat_backup_runner` | LOGIN; BACKUP on database `jandibat`; USAGE on `jandibat_backup_v1` without grant option. |
| `jandibat_backup_verifier` | LOGIN; USAGE on `jandibat_backup_v1` without grant option; no metadata access. |

The built-in root ALL connection row is expected. No SQL admin membership,
RESTORE, application-table mutation or runner/verifier SYSTEM grant is allowed.
Creator DROP is an unavoidable ownership capability, not permission for the
wrapper to drop or rotate a connection. Start the fixture without
EXTERNALIOIMPLICITACCESS; only a reproducible native x86 S3 privilege denial can
justify adding this previously contemplated grant to bootstrap, with independent
review and explicit exact-grant tests. Any other privilege expansion stops the
implementation for a design decision; never retry as root.

The view depends on the pinned, unsupported internal catalog encoding and the
unsafe-internals session switch. Read only `sha256(connection_details)`, never
the bytes, URI or decoded protobuf. No catalog writes are permitted. Version or
digest-format changes fail closed and require reviewed compatibility proof before
an upgrade. The pinned source declares the raw details as bytes; the stable public
SQL contract does not promise this encoding. [Cockroach v26.2.5 system schema](https://github.com/cockroachdb/cockroach/blob/v26.2.5/pkg/sql/catalog/systemschema/system.go).

## Input, transaction and fail-closed interface

`scripts/db-bootstrap-backup-connection.sh` accepts only the bootstrap DSN through
`BACKUP_BOOTSTRAP_DATABASE_URL` and these provider-neutral values through the
phase Secret environment/files: `BACKUP_S3_ENDPOINT`, `BACKUP_S3_REGION`,
`BACKUP_S3_BUCKET`, `BACKUP_S3_PREFIX`, `BACKUP_S3_PATH_STYLE`,
`BACKUP_S3_ACCESS_KEY_ID`, `BACKUP_S3_SECRET_ACCESS_KEY`. Use the existing env
interface initially; do not add provider-specific SDK or B2 assumptions.

Validate before SQL: HTTPS endpoint with hostname and optional port, no userinfo,
query, fragment or path; nonempty region/bucket/prefix; path style exactly `true`
or `false`; no control characters in any value. Region permits ASCII letters,
digits and hyphens; bucket permits lowercase letters, digits, dots and hyphens;
prefix permits ASCII letters, digits, slash, underscore and hyphen, with no empty,
`.` or `..` segments or leading/trailing slash. Reject invalid authority/port,
pre-encoded endpoint text and malformed input rather than normalizing it silently.
Credentials may contain punctuation and spaces; encode their UTF-8 bytes once
with RFC 3986 unreserved bytes preserved and uppercase percent escapes.

Construct one deterministic URI with query keys in this order:
`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_ENDPOINT`, `AWS_REGION`,
`AWS_USE_PATH_STYLE`. Percent-encode each query value. Define `input_digest` as
lowercase SHA-256 hex of UTF-8 `jandibat-backup-policy-v1\n` followed by the exact
canonical URI, with no final newline. Hash stdin, never command arguments. Any
credential or endpoint/policy change must change this digest. Store neither URI
nor credential in application metadata; the engine's external-connection record
necessarily contains the credential. Treat both digests as private derived data,
never public metrics, logs or review receipts.

Authenticate `current_user()` as bootstrap. Enable unsafe-internals explicitly
in every SQL process reading the digest view. In one consistent read transaction,
classify the fixed name and metadata:

| Live row | Metadata row | Action |
| --- | --- | --- |
| Absent | Absent | Fresh-create path only. |
| Exactly one | Exactly one, version 1 and both digests match | Check connectivity and finish/verify the allowed grants. |
| Any other count, missing side, malformed digest/version or mismatch | Any | Nonzero fixed diagnostic; no CREATE/ALTER/DROP, metadata mutation or grants. |

Fresh-create transaction on a single connection:

1. `BEGIN`; CREATE fixed-name external connection from validated URI over stdin.
2. INSERT exactly one version-1 metadata row using the local input digest and a
   scalar SELECT of `catalog_digest` from the root-owned view. A missing digest
   violates NOT NULL and rolls back; never use an INSERT SELECT that can insert
   zero rows successfully.
3. `COMMIT`. A failure before commit rolls back both SQL objects. No UPSERT,
   ON CONFLICT, IF NOT EXISTS, metadata UPDATE or automatic adoption.

The pre-read is not a lock. Concurrent CREATE/INSERT conflicts must leave at most
one pair; a loser exits nonzero and a later explicit rerun reclassifies it.
Serialization errors or uncertain COMMIT responses must not replay secret SQL
blindly. Exit nonzero; rerun reads the current pair before taking any action.

After committed creation, or after an exact matching rerun, issue
`CHECK EXTERNAL CONNECTION 'external://jandibat_backup_v1' WITH transfer = '1MiB'`.
Parse the actual v26.2.5 result schema, require at least one row and success for
every reported node; unexpected shape/empty rows fail. This check performs storage
I/O; it is not a read-only verifier operation. A check failure preserves the pair
and gives no new USAGE grants. A later matching rerun can repeat the check.

Re-read both digests after CHECK. Reject unexpected grants or ownership; absent
runner/verifier USAGE is the only resumable incomplete grant state. Grant the two
USAGE privileges together in one transaction, then verify the full exact grant
set and both digests again before fixed success output. This final postcondition
does not create a lock against an administrator changing the connection later.
Require operational serialization with any separately approved rotation; a
detected race fails without declaring readiness, and the next phase must depend
on successful verification. Never repair drift by changing stored digests.

These checks detect accidental input/catalog drift at verification time. They
are not protection against a malicious root or compromised bootstrap principal,
which already possesses connection authority and credentials. SQL transaction
rollback cannot undo remote validation/check objects; inspect synthetic storage
side effects in the fixture and document cleanup without granting deletion power
to unrelated actors.

## Server log contract

The pinned probe showed access IDs are treated differently from secret keys by
OPS CREATE logging. `redact: true` alone is insufficient. Adopt the following
tested channel policy for the fixture first; its production equivalent must be
reviewed and rendered separately in homelab:

```yaml
file-defaults:
  redact: false
  buffered-writes: false
sinks:
  file-groups:
    default:
      channels:
        INFO: "all except [OPS, SENSITIVE_ACCESS, USER_ADMIN, PRIVILEGES, SESSIONS, SQL_EXEC]"
    ops:
      channels:
        WARNING: [OPS]
    sql-audit:
      channels: [SENSITIVE_ACCESS]
      auditable: true
    security:
      channels: [USER_ADMIN, PRIVILEGES]
      auditable: true
    sql-auth:
      channels: [SESSIONS]
      auditable: true
    sql-exec:
      channels: [SQL_EXEC]
      auditable: true
  stderr:
    channels: "all except OPS"
    filter: INFO
    redact: false
```

Keep capture-stray-errors enabled. This exact tested config had no network sinks.
An added Fluent/HTTP/OTLP sink must exclude OPS INFO too and pass the same sentinel
proof. Do not disable SQL/security/sensitive audit channels or raise the global
threshold. `redact: false` here describes the proven baseline, not a requirement
to turn off unrelated production redaction; any stricter variant needs the same
positive audit and negative sentinel checks.

Tradeoff: OPS INFO diagnostics are unavailable, and this baseline routes OPS
WARNING+ only to the dedicated file, not container stderr. SQL/security audit
events remain available. Production log collection must include the dedicated
OPS file if operational warning visibility depends on a central collector.
Do not claim audit coverage just because a sink is configured: the fixture enables
`sql.log.all_statements.enabled=true` and checks actual CREATE SQL, security and
denied-sensitive-read events, without requiring the unlicensed table-audit feature.
Homelab must explicitly document the effective SQL audit settings it retains;
the fixture's extra SQL statement logging is not silently a production change.

Validate the effective config with the pinned `cockroach debug check-log-config`.
Cockroach reads this config at startup and file-group overrides replace the
default group structure; verify all sinks after rendering, not a partial YAML
snippet. [Cockroach log configuration](https://www.cockroachlabs.com/docs/v26.2/configure-logs).

The bootstrap cannot attest server logging through a caller-supplied environment
flag. Homelab must gate the connection Job on the DB rollout using this reviewed
config, and retain evidence of the effective config and pod-template revision.
Do not run credential-bearing CREATE against an old/default-log node. Local
script tests alone never establish that deployment prerequisite.

## Non-output, fixture and rollback boundaries

All DSNs use `COCKROACH_URL` environment only; SQL uses stdin. No `--url`,
`--execute` containing values, shell trace, URI in argv, raw SHOW CREATE, raw
catalog output, or error interpolation. Use private captures under `umask 077`,
cleanup on success/error/signal, bounded capture sizes and fixed diagnostics.
Only validated five-character SQLSTATEs, phase names and booleans may leave the
fixture. No secret/digest/full URI appears in chat, commits or uploaded artifacts.

Native x86_64-linux proof uses the existing disposable TLS Cockroach image
`cockroachdb/cockroach:v26.2.5@sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282`
and Nix-pinned authenticated HTTPS S3Proxy. No existing DB/PVC, real B2 credentials
or remote production endpoint. Non-root clients mount CA-only material and their
own phase env; only root bootstrap mounts the root client key. Darwin exit 77
is SKIP, never PASS. Success requires actual CREATE and CHECK under bootstrap,
second-run no-op, precise grants, rollback/partial/race/drift tests, and scans of
all client/server/container/S3Proxy sinks on both success and failure. Scan raw
and percent-encoded sentinel forms; keep the existing all-server-log scanner.
First demonstrate default logging RED, then policy-configured GREEN with positive
audit markers. Missing logs/scans/cleanup evidence fail the gate.

Operational rollback stops advancement and preserves the connection/metadata.
Do not automatically drop either on CHECK failure, drift or uncertain commit.
Do not revert the logging policy to leaky defaults while CREATE/ALTER or retry is
possible. A logging rollout can restart the single DB node; any real rollout
needs an exact inventory, outage/rollback plan and separate approval. Restore or
rotation requires reviewed targets and disposition, not a metadata rewrite.
Never automatically delete a real DB/PVC or backup object to recover this phase.

The resulting Task 2 receipt records source/image revision, native architecture,
test names/statuses, observed SQLSTATEs, exact grant names, all-sink sentinel
booleans, positive audit booleans and cleanup status only. H2 remains open until
later full/incremental chain and isolated restore gates prove measured recovery.
