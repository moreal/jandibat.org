# Staging deployment

현재 상태는 **구성 완료, 실제 환경 미검증**입니다. 실제 실행 증거가 없으므로 이 파일만으로 Phase 3 staging gate가 통과한 것으로 간주하지 않습니다.

## Host 준비

배포 host에는 Docker Engine, Docker Compose v2, `curl`, `tar`가 필요합니다. repository의 `deploy/staging/.env.staging.example`을 host의 `deploy/staging/.env.staging`으로 복사한 뒤 secret manager 값으로 채우고 권한을 `0600`으로 제한합니다. 이 파일은 저장소나 CI artifact에 업로드하지 않습니다.

DB credential은 역할을 분리합니다.

- `DATABASE_URL`: `jandibat_api` API runtime의 HTTP/auth/audit-insert/enqueue 권한
- `WORKER_DATABASE_URL`: `jandibat_worker` sync worker의 provider/sync/fact 최소 권한
- `MAINTENANCE_DATABASE_URL`: `jandibat_maintenance` retention/re-encryption 최소 권한
- `MIGRATION_DATABASE_URL`: schema migration 전용 권한
- `BACKUP_DATABASE_URL`: external connection 검사, backup, 격리 restore 전용 권한

API, worker, maintenance container에는 각각 자기 URL 하나만 전달됩니다. 다른 runtime 역할의 credential을 교차 주입하지 않습니다. 세 production executable은 URL의 username을 각각 `jandibat_api`, `jandibat_worker`, `jandibat_maintenance`로 검사해 잘못된 역할이면 listener를 열기 전에 종료합니다. Migration·backup URL은 각각 일회성 tool container에만 전달됩니다.

세 runtime 사용자는 배포 전에 secret manager/DB 관리 절차로 각각 독립 credential을 가진 `LOGIN` user로 생성해야 합니다. 배포는 migration 뒤 `scripts/db-configure-runtime-roles.sh`를 실행해 table별 allowlist를 재적용하며, 사용자가 없거나 `NOLOGIN`이면 중단합니다. 정확한 권한과 negative test는 `docs/runbooks/DATABASE_ROLES.ko.md`를 따릅니다.

동일한 API image를 세 process로 실행합니다.

| Service | Executable | Health |
| --- | --- | --- |
| `api` | `/jandibat-api` (image 기본 entrypoint) | `:8080/livez`, `:8080/readyz`, `:8080/healthz` |
| `worker` | `/jandibat-worker` | 내부 `:8081/livez`, `:8081/readyz`, `:8081/healthz` |
| `maintenance` | `/jandibat-maintenance` | 내부 `:8082/livez`, `:8082/readyz`, `:8082/healthz` |

Worker/maintenance health port는 host에 publish하지 않으며 배포 smoke가 container 내부에서 확인합니다. `DEVELOPMENT_ALL_IN_ONE`은 production에서 항상 `false`입니다.

## 수동 검증과 배포

Compose 해석은 secret이 없는 예제 값으로 확인할 수 있습니다.

```sh
make staging-compose-check
```

실제 host에서는 immutable digest reference를 사용합니다.

```sh
export API_IMAGE='ghcr.io/owner/repository/api@sha256:...'
export WEB_IMAGE='ghcr.io/owner/repository/web@sha256:...'
export BUILD_SHA='<full-40-character-release-sha>'
export REGION='<staging-region>'
export STAGING_ENV_FILE="$PWD/deploy/staging/.env.staging"
make deploy-staging
```

배포 스크립트는 image pull → backup 검증 → checksum migration → runtime GRANT 재적용 → API/worker/maintenance/Web 교체 → 네 process readiness smoke 순서로 실행합니다. smoke가 실패하면 이전 application image를 복구합니다. 이미 적용된 expand migration은 되돌리지 않으므로, 배포 전후 image가 같은 schema와 호환돼야 합니다. 최초 배포가 실패해 이전 image가 없으면 실패한 container를 중지합니다.

## GitHub Actions environment

`.github/workflows/deploy-staging.yml`은 `staging` environment 승인 뒤 수동 실행합니다. 다음 secret이 필요합니다.

- `STAGING_SSH_PRIVATE_KEY`, `STAGING_SSH_KNOWN_HOSTS`
- `STAGING_SSH_HOST`, `STAGING_SSH_USER`, `STAGING_DEPLOY_DIR`
- `GHCR_USERNAME`, `GHCR_PULL_TOKEN`
- `STAGING_API_URL`, `STAGING_WEB_URL`, `STAGING_PUBLIC_SUBJECT`
- custom-ingest load까지 검증할 경우 `STAGING_CUSTOM_PROVIDER_ID`, `STAGING_CUSTOM_PROVIDER_KEY`
- repository/environment variable `STAGING_REGION` (secret이 아닌 관측 label)

Workflow는 API/Web image를 각각 빌드해 GHCR에 push하고 tag가 아닌 registry digest로 원격 배포합니다. 배포 뒤 security smoke, 30분 요청률 load gate, latest-backup 격리 restore를 실행하고 선택적으로 직전 digest rollback/re-promotion과 API/worker termination fault rehearsal을 수행합니다. JSON/log 결과는 commit SHA가 포함된 staging evidence artifact로 보존합니다. 최초 배포처럼 직전 release가 없으면 rollback rehearsal input을 끄고 증거에 `NOT RUN`과 이유를 기록해야 하며 Phase 3 gate는 아직 통과하지 않습니다. SSH host key는 `ssh-keyscan` 결과를 실행 중에 즉석 신뢰하지 말고, 별도 안전한 채널에서 검증한 값을 `STAGING_SSH_KNOWN_HOSTS`에 저장합니다.

실행 뒤 commit SHA, 두 image digest, migration/backup/load 결과와 rollback 여부를 `docs/evidence/staging/YYYY-MM-DD-<short-sha>/README.md`에 기록해야 검증 완료입니다.
