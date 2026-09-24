# Deploy, Rollback 및 Migration Runbook

기준일: 2026-08-13

이 runbook은 immutable API/Web image 배포, API image의 분리된 HTTP/sync/maintenance process, checksum migration, staging promotion과 rollback 기준을 정의합니다. 저장소에는 image build, checksum migration, backup, 배포와 application rollback 자동화가 있습니다. 다만 실제 staging 실행 기록은 아직 없으므로 운영 검증 상태는 `NOT VERIFIED`입니다.

## 1. Release 불변 조건

- release 단위는 Git commit SHA와 API/Web image digest 두 개로 식별합니다.
- tag만으로 배포하지 않고 digest를 manifest에 고정합니다.
- 동일 SHA의 image를 staging에서 production으로 promote하며 다시 build하지 않습니다.
- API 변경은 OpenAPI, generated TypeScript type, interface change log와 함께 배포합니다.
- migration은 expand → compatible application → contract 순서로 최소 두 release에 나눕니다.
- production migration 전 24시간 이내 성공 backup과 최근 7일 이내 restore drill이 필요합니다.
- migration과 deploy 실행자는 분리된 최소 권한 identity를 사용합니다.
- 실제 secret은 image layer, build arg, CI log, evidence에 포함하지 않습니다.

## 2. 사전 점검

```sh
git status --short
git rev-parse HEAD
make ci
make staging-compose-check
test -f apps/api/Dockerfile
test -f apps/web/Dockerfile
test -f scripts/db-migrate-url.sh
test -f scripts/deploy-staging.sh
```

`git status --short`는 비어 있어야 합니다. 이어서 release SHA를 고정합니다.

```sh
RELEASE_SHA="$(git rev-parse HEAD)"
test -n "$RELEASE_SHA"
```

CI에서 다음이 모두 green이어야 합니다.

- OpenAPI lint/generated drift.
- Go test와 race test.
- frontend typecheck/build/test.
- 실제 CockroachDB v26.2에서 migration apply/verify/reapply.
- dependency, code, secret, container scan에서 신규 high/critical 0건.
- API/Web image build와 SBOM/provenance 생성.

## 3. Immutable image build

구현된 Dockerfile 기준 명령:

```sh
docker build --pull \
  --file apps/api/Dockerfile \
  --tag "ghcr.io/moreal/jandibat-api:$RELEASE_SHA" \
  apps/api

docker build --pull \
  --file apps/web/Dockerfile \
  --tag "ghcr.io/moreal/jandibat-web:$RELEASE_SHA" \
  .
```

각 image는 non-root user와 explicit healthcheck를 정의합니다. API image에는 `/jandibat-api`, `/jandibat-worker`, `/jandibat-maintenance` 세 executable이 있으며 staging Compose가 별도 container/DB role로 실행합니다. 각 runtime container에는 자기 DSN만 주입하고 read-only root filesystem, capability drop, `no-new-privileges`를 적용합니다. CI가 registry push 후 반환한 manifest digest를 기록하고 이후 deploy는 다음 형태로 digest를 사용합니다. Dockerfile과 Compose base image는 사람이 읽을 수 있는 정확한 version tag와 multi-arch manifest digest를 함께 고정하며, 의존성 갱신 PR이 둘을 같이 검증·갱신합니다.

```text
ghcr.io/moreal/jandibat-api@sha256:<digest>
ghcr.io/moreal/jandibat-web@sha256:<digest>
```

로컬 build 성공은 registry artifact provenance나 staging 실행을 대체하지 않습니다.

## 4. Checksum migration runner 계약

원격/staging migration은 `scripts/db-migrate-url.sh` 또는 다음 Make target으로 실행합니다.

```sh
MIGRATION_DATABASE_URL="$STAGING_MIGRATION_DATABASE_URL" make db-migrate-url
MIGRATION_DATABASE_URL="$STAGING_MIGRATION_DATABASE_URL" make db-migrate-url
```

첫 실행은 pending migration을 적용하고, 두 번째 실행은 모든 항목을 `already applied`로 보고해야 합니다. 로컬 Compose DB에는 `make db-migrate`를 사용합니다.

Migration runner는 각 파일과 `schema_migrations` ledger INSERT를 `autocommit_before_ddl=off`인 한 Cockroach transaction/session에서 실행합니다. Schema-locked table을 변경해야 하는 migration은 검증된 `-- jandibat:schema-unlock <table>` directive를 한 개만 선언합니다. Runner는 table identifier를 allowlist 형식으로 검증하고 별도 implicit transaction에서 unlock한 뒤 DDL+ledger를 원자적으로 적용하며 성공·실패·다음 재실행 모두에서 relock합니다. `scripts/db-verify-schema-locks.sh`는 모든 directive table을 독립 process에서 검증하고, migration process가 SIGKILL되어 trap을 실행하지 못한 경우에만 배포 실패 경로가 `--repair`로 같은 allowlist를 재잠급니다. 배포 성공 뒤에도 verify-only 단계를 실행하고 결과를 staging evidence에 보존합니다. `make migration-atomicity-test`는 일반 DDL 오류와 constraint 교체 중 오류의 rollback뿐 아니라 trap이 실행되지 않은 unlocked 상태의 fail-closed 탐지·독립 복구를 확인합니다. Fixture 경로는 runtime migration directory와 분리되어 있습니다.

필수 동작:

- `db/migrations/*.sql`을 numeric version 순으로 적용.
- migration마다 filename을 version으로 사용하고 SHA-256 checksum과 적용 시각을 `schema_migrations`에 기록.
- 이미 적용된 version의 현재 file checksum이 다르면 즉시 non-zero 종료. 덮어쓰기 금지.
- migration SQL과 checksum 기록은 CockroachDB transaction semantics에 의존하므로 DDL은 forward-compatible하고 재실행 가능하게 작성합니다.
- 실패 migration을 success로 기록하지 않고 version만 출력합니다. SQL에 secret/data를 넣거나 출력하지 않습니다.
- 재실행은 이미 성공한 version의 checksum을 검증한 뒤 건너뜁니다.
- URL이 가리키는 database가 `COCKROACH_DATABASE`와 다르면 즉시 중단합니다.

DDL rollback용 임의 `down` 명령은 제공하지 않습니다. 잘못된 migration은 호환 가능한 새 forward migration으로 수정합니다.

## 5. Migration 설계 규칙

Expand release:

- nullable column/table/index 추가처럼 old application이 무시할 수 있는 변경만 수행.
- backfill은 DDL과 분리하고 batch/checkpoint/rate limit 적용.
- 새 constraint는 기존 data 검증 뒤 활성화.
- large index/schema change의 job 상태와 DB 부하를 관찰.

Application release:

- old/new schema를 모두 읽을 수 있고 write path 전환은 feature flag/canary로 수행.
- 최소 한 release 동안 dual-read 또는 필요한 dual-write를 유지.

Contract release:

- old application instance가 0이고 rollback window가 끝난 뒤 old column/index 제거.
- 제거 migration 전 fresh backup/restore evidence 필요.
- contract migration 이후 application rollback이 불가능하면 release note에 명시하고 Incident Commander 승인을 요구.

## 6. Staging 배포 순서

현재 제공되는 single-host staging interface:

```sh
API_IMAGE="ghcr.io/moreal/jandibat-api@sha256:$API_DIGEST" \
WEB_IMAGE="ghcr.io/moreal/jandibat-web@sha256:$WEB_DIGEST" \
BUILD_SHA="$RELEASE_SHA" \
REGION="$STAGING_REGION" \
STAGING_ENV_FILE="$PWD/deploy/staging/.env.staging" \
make deploy-staging
```

스크립트는 digest 형식을 검증하고 pull → backup → checksum migration → runtime GRANT 재적용 → API/worker/maintenance/Web 교체 → API/Web host health와 worker/maintenance 내부 readiness smoke를 수행합니다. 실패하면 이전 application image를 복구하며 이미 적용된 forward migration은 되돌리지 않습니다. 현재 single-host Compose 자동화에는 weighted 5%/25% traffic splitting이 없으므로 아래 canary 관찰은 staging ingress/orchestrator에서 별도로 수행하고 증거를 남겨야 합니다.

순서:

1. staging exact Cockroach version과 backup 상태 확인.
2. `scripts/deploy-staging.sh`의 backup과 migration을 실행하고, 별도 migration 검증이 필요하면 `make db-migrate-url`을 재실행해 checksum/idempotency 확인.
3. migration 직후 기존 API image로 5분 smoke해 backward compatibility 확인.
4. 새 API image 1 instance/canary 배포, 10분 관찰.
5. API 100% 전환 후 새 Web image 배포.
6. synthetic smoke와 GraphQL SDL domain contract/OpenAPI HTTP edge contract test.
7. 30분 Phase 3 load test와 지정 fault/security subset.
8. rollback rehearsal로 직전 API/Web image를 배포했다가 다시 candidate로 복귀.
9. evidence와 승인 기록 후에만 production promotion 가능.

## 7. Smoke 및 migration 호환성 검증

```sh
curl --fail --silent "$STAGING_BASE_URL/healthz"
jq -n --arg subject "$STAGING_FIXTURE_SUBJECT" --arg day "$(date -u +%Y-%m-%d)" \
  '{query:"query StagingSnapshot($subject:String!,$range:DateRangeInput!,$timezone:TimeZone!){subject(handleOrID:$subject){handle activitySnapshot(range:$range,timezone:$timezone){revision generatedAt dataUpdatedAt}}}",operationName:"StagingSnapshot",variables:{subject:$subject,range:{from:$day,to:$day},timezone:"UTC"}}' \
  | curl --fail --silent --show-error --request POST \
      --header 'Content-Type: application/json' --data-binary @- "$STAGING_BASE_URL/graphql" \
  | jq -e --arg subject "$STAGING_FIXTURE_SUBJECT" \
      '(.errors == null) and (.data.subject.handle == $subject) and (.data.subject.activitySnapshot.revision | type == "string" and length > 0)'
curl --fail --silent \
  "$STAGING_BASE_URL/v1/render/$STAGING_FIXTURE_SUBJECT.svg" \
  | xmllint --noout -
```

인증 fixture로 Magic Link consume/replay 거부, Passkey synthetic ceremony, provider 연결/revoke, custom ingestion idempotency, scheduler enqueue/job 상태, 감사 event correlation을 확인합니다.

Migration CI matrix:

| DB 상태 | Application | 기대 |
| --- | --- | --- |
| migration 전 | old image | 정상 |
| expand 적용 후 | old image | 정상 |
| expand 적용 후 | new image | 정상 |
| backfill 중 | old/new image | 정상, 중복/누락 0 |
| contract 적용 후 | new image | 정상 |

contract 적용 후 old image는 의도적으로 지원하지 않을 수 있으나, 그 시점에는 rollback 대신 forward-fix만 가능하다는 승인이 필요합니다.

## 8. Canary와 promotion 기준

단계는 5% 10분 → 25% 10분 → 100% 30분입니다. 각 단계에서 다음을 모두 만족해야 합니다.

- `5xx` 0.1% 이하이고 baseline 대비 0.1 percentage point 이상 증가하지 않음.
- route별 p95/p99가 `docs/SLO.ko.md` gate 이내.
- readiness 실패, crash loop, OOM 0건.
- credential decrypt/audit persist failure 0건.
- DB pool exhaustion 0건, transaction retry 최종 실패 0.1% 이하.
- queue oldest age 30분 미만, 동일 connection 동시 sync 0건.
- synthetic test 100% 성공.

관찰 시간이 끝나지 않았으면 성공으로 간주하지 않습니다.

## 9. Rollback 기준과 명령

다음 중 하나면 자동 promotion을 중단하고 Release Owner가 5분 안에 rollback을 결정합니다.

- 5분 window `5xx` 2% 초과 또는 SLO multi-window page.
- p99가 baseline의 2배이면서 SLO 초과가 5분 지속.
- authorization/privacy 위반 또는 secret 노출 1건.
- audit critical event 누락 1건.
- decrypt failure, migration checksum mismatch, data invariant 위반 1건.
- crash loop/OOM 또는 DB saturation이 5분 지속.

직전 digest로 명시적으로 되돌릴 때의 interface:

```sh
API_IMAGE="$PREVIOUS_API_IMAGE_DIGEST" \
WEB_IMAGE="$PREVIOUS_WEB_IMAGE_DIGEST" \
BUILD_SHA="$PREVIOUS_RELEASE_SHA" \
REGION="$STAGING_REGION" \
STAGING_ENV_FILE="$PWD/deploy/staging/.env.staging" \
make deploy-staging
```

Rollback 뒤:

1. API `/healthz` dependency readiness와 Web `/healthz`, synthetic smoke를 10분 수행.
2. 신규 writes를 중단해야 했는지, data repair가 필요한지 확인.
3. expand migration은 그대로 두고 old image가 호환되게 유지.
4. migration checksum row를 삭제하거나 applied SQL을 수동으로 되돌리지 않음.
5. contract migration 때문에 old image가 실행 불가하면 rollback을 시도하지 않고 canary traffic을 0으로 내린 뒤 forward-fix image/migration을 배포.
6. `deployment.rollback` 감사 이벤트와 incident/ticket을 기록.

## 10. Production promotion

다음 증거가 모두 있어야 합니다.

- 동일 image digest의 staging canary/load/rollback rehearsal 성공.
- migration matrix와 checksum artifact 성공.
- 최근 24시간 backup과 최근 7일 restore drill 성공.
- 신규 high/critical security finding 0 또는 유효한 risk acceptance.
- error budget이 25% 이상 남아 있음. security emergency는 Incident Commander가 예외 승인 가능.
- on-call과 rollback operator가 release window에 참여.

Production도 migration verify/apply/verify → API canary → API promotion → Web deploy 순서를 사용합니다. 배포 후 30분 관찰 전 release를 종료하지 않습니다.

## 11. 증거

`docs/evidence/staging/README.md` 템플릿에 다음을 기록합니다.

- commit SHA, API/Web digest, SBOM/provenance/scan 링크.
- Cockroach exact version, migration 전후 version/checksum.
- backup/restore evidence.
- 각 deploy/canary/rollback 명령의 UTC 시각, exit code, CI/run URL.
- smoke, contract, load, fault, security 결과.
- SLO 실측치와 dashboard snapshot.
- promotion/rollback 결정과 승인자.

placeholder, 빈 링크, `NOT RUN` 항목이 있으면 staging 검증 미완료입니다.
