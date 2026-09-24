# Staging 운영 검증 증거

현재 상태: **NOT VERIFIED**

기준일: 2026-08-12

이 디렉터리에는 Phase 3 운영 검증의 재현 가능한 실행 기록을 보관합니다. 현재 이 README는 형식만 정의하며 실제 staging 실행을 증명하지 않습니다. 자동 테스트 결과 링크, staging 실행 기록, 해당 runbook 링크가 모두 있고 승인 조건을 통과해야만 `docs/DELIVERY_CHECKLIST.ko.md`의 운영 항목을 완료할 수 있습니다.

## 1. 기록 규칙

실행마다 다음 경로를 추가합니다.

```text
docs/evidence/staging/YYYY-MM-DD-<short-sha>/README.md
```

예:

```text
docs/evidence/staging/2026-08-12-a1b2c3d/README.md
```

- UTC 시각을 사용합니다.
- commit SHA, API/worker/maintenance/web 및 restore-tools image digest, CockroachDB exact version을 고정합니다. 각 digest와 일치하는 SPDX/Syft/Grype evidence와 Linux archive 재빌드·runtime 로그를 연결합니다.
- CI/run/dashboard 링크는 조직 권한 안에서 최소 400일 유지합니다.
- raw log가 크면 immutable CI artifact/object storage에 저장하고 SHA-256과 expiry를 기록합니다.
- token, cookie, email, 실제 user/subject ID, ciphertext, external connection URI를 넣지 않습니다.
- 실행하지 않은 항목은 빈칸이나 `N/A`로 숨기지 말고 `NOT RUN`으로 표시합니다.
- 실패 실행도 삭제하지 않고 실패 원인과 후속 issue를 기록합니다.
- 수동으로 checkbox만 바꾸지 않고 링크된 증거를 검토한 승인자가 서명합니다.

## 2. 실행 README 템플릿

아래 내용을 새 실행 디렉터리의 `README.md`로 복사합니다.

```markdown
# Phase 3 staging evidence — YYYY-MM-DD / <short-sha>

Status: NOT RUN | RUNNING | FAILED | PASSED

## Identity

| Field | Value |
| --- | --- |
| Environment | staging |
| Started at UTC | NOT RUN |
| Finished at UTC | NOT RUN |
| Operator | NOT RUN |
| Observer | NOT RUN |
| Release approver | NOT RUN |
| Commit SHA | NOT RUN |
| API image digest | NOT RUN |
| Worker image digest | NOT RUN |
| Maintenance image digest | NOT RUN |
| Web image digest | NOT RUN |
| Restore-tools image digest | NOT RUN |
| CockroachDB version | NOT RUN |
| Change/incident ID | NOT RUN |

## Immutable artifacts

| Artifact | URL | SHA-256 / digest | Expiry | Result |
| --- | --- | --- | --- | --- |
| `make ci` | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| API SBOM/provenance | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| Web SBOM/provenance | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| container/security scan | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| migration report | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| load result | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| fault test result | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| DAST/security result | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| backup/restore result | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| dashboard snapshot | NOT RUN | NOT RUN | NOT RUN | NOT RUN |

## Migration

Runbook: `docs/runbooks/DEPLOY_ROLLBACK_MIGRATION.ko.md`

| Check | Before | After | Result/evidence |
| --- | --- | --- | --- |
| `MIGRATION_DATABASE_URL=... make db-migrate-url` | NOT RUN | NOT RUN | NOT RUN |
| applied versions | NOT RUN | NOT RUN | NOT RUN |
| checksum list | NOT RUN | NOT RUN | NOT RUN |
| reapply idempotency | NOT RUN | NOT RUN | NOT RUN |
| old image + expanded schema | NOT RUN | NOT RUN | NOT RUN |
| new image + expanded schema | NOT RUN | NOT RUN | NOT RUN |
| unfinished/duplicate migration | NOT RUN | NOT RUN | NOT RUN |

Commands, UTC, exit codes:

```text
NOT RUN
```

## Deploy and rollback rehearsal

Runbook: `docs/runbooks/DEPLOY_ROLLBACK_MIGRATION.ko.md`

| Stage | Started/finished UTC | Image digest | Result | Evidence |
| --- | --- | --- | --- | --- |
| API 5% canary | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| API 25% | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| API 100% | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| Web deploy | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| previous-image rollback | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| candidate re-promotion | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| 30m observation | NOT RUN | NOT RUN | NOT RUN | NOT RUN |

Rollback trigger observed: NOT RUN

Rollback/re-promotion decision: NOT RUN

## Smoke and contract

| Test | Result | Evidence |
| --- | --- | --- |
| API `/healthz` dependency readiness 5m | NOT RUN | NOT RUN |
| Web `/healthz` liveness 5m | NOT RUN | NOT RUN |
| public activity fixture | NOT RUN | NOT RUN |
| SVG validation | NOT RUN | NOT RUN |
| Magic Link expiry/replay | NOT RUN | NOT RUN |
| Passkey synthetic ceremony | NOT RUN | NOT RUN |
| provider connect/revoke | NOT RUN | NOT RUN |
| custom ingest idempotency | NOT RUN | NOT RUN |
| scheduler duplicate isolation | NOT RUN | NOT RUN |
| audit correlation | NOT RUN | NOT RUN |

## SLO, load, fault and security

Policy: `docs/SLO.ko.md`

| Gate | Target | Measured | Result/evidence |
| --- | --- | --- | --- |
| public activity p95/p99 | 300ms / 800ms | NOT RUN | NOT RUN |
| SVG p95/p99 | 500ms / 1.2s | NOT RUN | NOT RUN |
| mutation p95/p99 | 500ms / 1.5s | NOT RUN | NOT RUN |
| custom ingest p95/p99 | 700ms / 2s | NOT RUN | NOT RUN |
| server error ratio | <= 0.1% | NOT RUN | NOT RUN |
| duplicate facts/jobs | 0 | NOT RUN | NOT RUN |
| audit critical loss | 0 | NOT RUN | NOT RUN |
| load profile 30m | specified profile | NOT RUN | NOT RUN |
| provider 429/5xx isolation | pass | NOT RUN | NOT RUN |
| API/worker termination | pass | NOT RUN | NOT RUN |
| Cockroach/DB connection fault | pass | NOT RUN | NOT RUN |
| security high/critical | 0 new | NOT RUN | NOT RUN |

Open findings and risk acceptances: NOT RUN

## Key/token rotation

Runbook: `docs/runbooks/KEY_ROTATION.ko.md`

| Check | Result | Evidence |
| --- | --- | --- |
| old/new key dual decrypt | NOT RUN | NOT RUN |
| active write key switch | NOT RUN | NOT RUN |
| re-encryption dry-run | NOT RUN | NOT RUN |
| interrupted batch resume | NOT RUN | NOT RUN |
| old-key row/use count 0 | NOT RUN | NOT RUN |
| canary old-key removal | NOT RUN | NOT RUN |
| session signing overlap/retire | NOT RUN | NOT RUN |
| custom ingestion old key rejected | NOT RUN | NOT RUN |
| external connection credential rotation | NOT RUN | NOT RUN |
| required audit events | NOT RUN | NOT RUN |

Only key IDs and counts may be recorded. Secret values: NOT RECORDED

## Retention and deletion

Policy: `docs/DATA_RETENTION_AND_DELETION.ko.md`

| Check | Result | Evidence |
| --- | --- | --- |
| boundary fixtures dry-run | NOT RUN | NOT RUN |
| purge execute | NOT RUN | NOT RUN |
| interrupted purge resume | NOT RUN | NOT RUN |
| account deletion residual count 0 | NOT RUN | NOT RUN |
| custom-provider scoped deletion | NOT RUN | NOT RUN |
| legal hold preserved | NOT RUN | NOT RUN |
| backup expiry deadline recorded | NOT RUN | NOT RUN |

## Audit logging

Runbook: `docs/runbooks/AUDIT_LOGGING.ko.md`

| Check | Result | Evidence |
| --- | --- | --- |
| persistent append-only sink | NOT RUN | NOT RUN |
| API role UPDATE/DELETE denied | NOT RUN | NOT RUN |
| required fields/correlation | NOT RUN | NOT RUN |
| nested secret canary absent | NOT RUN | NOT RUN |
| critical mutation atomicity | NOT RUN | NOT RUN |
| sink outage fail-closed | NOT RUN | NOT RUN |
| 24h count reconciliation | NOT RUN | NOT RUN |

## Backup and restore

Runbook: `docs/runbooks/BACKUP_RESTORE.ko.md`

| Check | Target | Measured | Result/evidence |
| --- | --- | --- | --- |
| external connection all nodes | all `ok`, `can_delete` | NOT RUN | NOT RUN |
| latest backup/check_files | success | NOT RUN | NOT RUN |
| isolated `RESTORE ... FROM LATEST IN` | success | NOT RUN | NOT RUN |
| schema/checksum comparison | exact match | NOT RUN | NOT RUN |
| data/API smoke | all pass | NOT RUN | NOT RUN |
| deletion replay | residual 0 | NOT RUN | NOT RUN |
| RPO | <= 1h | NOT RUN | NOT RUN |
| RTO | <= 4h | NOT RUN | NOT RUN |

External connection name: NOT RUN

Backup/restore job IDs: NOT RUN

Storage URI/credential: NOT RECORDED

## Final decision

- [ ] Every required command has an immutable result link.
- [ ] Every result belongs to the commit/image/version above.
- [ ] No required row is `NOT RUN`, blank, or expired.
- [ ] No failed gate lacks a valid time-bounded risk decision.
- [ ] Rollback rehearsal passed.
- [ ] RPO/RTO and all deployment SLO gates passed.
- [ ] Security Owner approved key/audit/security evidence.
- [ ] Database Owner approved migration/backup/restore evidence.
- [ ] Release Owner approved promotion.

Decision: NOT RUN

Approvers and UTC timestamps: NOT RUN

Open issues: NOT RUN
```

## 3. 완료 판정

실행 README의 `Status: PASSED`만으로는 충분하지 않습니다. 다음을 독립적으로 확인합니다.

- 링크가 접근 가능하고 실행 SHA/image digest와 일치함.
- migration checksum과 CockroachDB version이 기록됨.
- rollback rehearsal이 실제 이전 digest를 사용함.
- load/fault/security 결과가 `docs/SLO.ko.md` 수치를 충족함.
- key rotation, retention/deletion, audit, backup/restore runbook의 모든 필수 단계가 실행됨.
- RPO/RTO가 raw timestamp로 재계산 가능함.
- secret과 실제 사용자 데이터가 증거에 없음.

하나라도 증거가 약하거나 누락되면 Phase 3 운영 상태는 미완료로 유지합니다.
