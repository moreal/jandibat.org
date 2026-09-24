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

API, worker, maintenance, web은 각각 별도의 Nix image archive와 기본 entrypoint를 사용합니다.

| Service | Executable | Health |
| --- | --- | --- |
| `api` | `/bin/server` | `:8080/livez`, `:8080/readyz`, `:8080/healthz` |
| `worker` | `/bin/worker` | 내부 `:8081/livez`, `:8081/readyz`, `:8081/healthz` |
| `maintenance` | `/bin/maintenance` | 내부 `:8082/livez`, `:8082/readyz`, `:8082/healthz` |
| `web` | `/bin/web-start` | `:8080/healthz` |

Worker/maintenance health port는 host에 publish하지 않으며 배포 smoke가 container 내부에서 확인합니다. `DEVELOPMENT_ALL_IN_ONE`은 production에서 항상 `false`입니다.

## 수동 검증과 배포

Compose 해석은 secret이 없는 예제 값으로 확인할 수 있습니다.

```sh
make staging-compose-check
```

실제 host에서는 immutable digest reference를 사용합니다.

```sh
export API_IMAGE='ghcr.io/owner/repository/api@sha256:...'
export WORKER_IMAGE='ghcr.io/owner/repository/worker@sha256:...'
export MAINTENANCE_IMAGE='ghcr.io/owner/repository/maintenance@sha256:...'
export WEB_IMAGE='ghcr.io/owner/repository/web@sha256:...'
export RESTORE_TOOLS_IMAGE='ghcr.io/owner/repository/restore-tools@sha256:...'
export BUILD_SHA='<full-40-character-release-sha>'
export REGION='<staging-region>'
export STAGING_ENV_FILE="$PWD/deploy/staging/.env.staging"
make deploy-staging
```

배포 스크립트는 image pull → backup 검증 → checksum migration → runtime GRANT 재적용 → positive/negative role 검증 → API/worker/maintenance/Web 교체 → 네 process readiness smoke 순서로 실행합니다. migration tool에는 `MIGRATION_DATABASE_URL`, `COCKROACH_DATABASE`, `MIGRATIONS_DIR`, 쓰기 가능한 `TMPDIR=/tmp`가 필수입니다. Web도 read-only root와 writable `/tmp`를 사용합니다. 실패하면 이전 네 workload와 restore-tools digest를 state 파일에서 복구합니다. 이미 적용된 expand migration은 되돌리지 않으므로, 배포 전후 image가 같은 schema와 호환돼야 합니다. 최초 배포가 실패해 이전 image가 없으면 실패한 container를 중지합니다.

기존 `.current-images`가 API/Web만 기록한 구형 형식이면 자동 rollback의 이전 상태로 사용하지 않습니다. 전환 전에 호환되는 네 Nix workload와 restore-tools digest가 모두 검증된 release를 준비하고 state를 기록해야 합니다. worker/maintenance 값을 이전 API image로 대신 채우면 entrypoint 계약이 맞지 않습니다.

## GitHub Actions environment

`.github/workflows/deploy-staging.yml`은 `staging` environment 승인 뒤 수동 실행합니다. 다음 secret이 필요합니다.

- `STAGING_SSH_PRIVATE_KEY`, `STAGING_SSH_KNOWN_HOSTS`
- `STAGING_SSH_HOST`, `STAGING_SSH_USER`, `STAGING_DEPLOY_DIR`
- `GHCR_USERNAME`, `GHCR_PULL_TOKEN`
- `STAGING_API_URL`, `STAGING_WEB_URL`, `STAGING_PUBLIC_SUBJECT`
- custom-ingest load까지 검증할 경우 `STAGING_CUSTOM_PROVIDER_ID`, `STAGING_CUSTOM_PROVIDER_KEY`
- repository/environment variable `STAGING_REGION` (secret이 아닌 관측 label)

Workflow는 Linux에서 네 Nix payload와 archive를 실제로 재빌드하고 read-only container smoke를 실행합니다. 가져온 image ID마다 SPDX SBOM과 high/critical Grype 검사를 수행하며 restore tooling도 포함합니다. GHCR의 full Git SHA tag는 lookup key이며 기존 digest가 후보와 다르면 push를 거부합니다. 처음 게시한 뒤에도 registry의 exact `name@sha256:<64 hex>`를 다시 검사하고 같은 digest에 대한 SPDX/Syft/Grype 증거가 모두 통과해야 deploy job이 시작됩니다. `staging-image-security-<SHA>` artifact에는 archive hash, image ID, manifest digest를 구별한 JSON과 재현성/runtime 로그가 포함됩니다. workflow concurrency가 이 저장소의 publisher를 직렬화하므로 다른 주체의 동일 tag 쓰기 권한도 제한해야 합니다.

Registry 부재 판단은 정확한 canonical NAME_UNKNOWN/MANIFEST_UNKNOWN 문구 또는 단일 오류 JSON의 허용된 형태만 인정합니다. 추가 오류·메시지·필드·출력 wrapper가 있으면 copy 전에 중단합니다. 실제 404라도 익숙하지 않은 GHCR/Skopeo 응답은 실패로 남기고, 실제 registry 증거를 검토한 뒤에만 허용 목록을 갱신합니다.

Restore archive 패키징은 Buildx 0.23 이상을 확인하고 기록한 뒤, digest가 고정된 BuildKit 서버를 사용하는 전용 `docker-container` builder를 생성·검증합니다. 생성 성공과 반환된 이름의 일치를 확인한 builder만 빌드 후 정리하며, host의 active/default builder 선택은 변경하지 않습니다. 생성 자체가 실패하면 다른 주체와의 이름 충돌 여부가 불확실하므로 가능한 부분 생성 상태도 삭제하지 않고 운영자의 확인을 위해 남깁니다.

배포 뒤 security smoke, 30분 요청률 load gate, latest-backup 격리 restore와 선택적 rollback/re-promotion·fault rehearsal을 수행합니다. 최초 배포처럼 직전 release가 없으면 rollback rehearsal input을 끄고 증거에 `NOT RUN`과 이유를 기록해야 하며 Phase 3 gate는 아직 통과하지 않습니다. SSH host key는 별도 안전한 채널에서 검증한 값을 `STAGING_SSH_KNOWN_HOSTS`에 저장합니다.

실행 뒤 commit SHA, 네 workload와 restore-tools의 다섯 image digest, 같은 digest의 SBOM/scan, migration/backup/load 결과와 rollback 여부를 `docs/evidence/staging/YYYY-MM-DD-<short-sha>/README.md`에 기록해야 검증 완료입니다. Darwin 정책 테스트는 실제 Linux 빌드·재현성·스캔 성공 증거를 대신하지 않습니다.
