# Immutable image 런타임 계약

이 문서는 homelab 배포 manifest가 따라야 할 process별 경계입니다. 설정 값의 형식과 전체 변수 목록은 [런타임 설정](CONFIGURATION.ko.md), DB 권한은 [Runtime DB 역할](runbooks/DATABASE_ROLES.ko.md)이 기준입니다. 네 배포 image는 서로 다른 Nix output이며 모두 숫자형 non-root 사용자로 시작합니다. 환경별 URL이나 credential은 image에 넣지 않습니다.

| Workload / 실행 파일 | Listen 및 probe | 주입할 DB 설정 | 주입할 secret 범위 |
| --- | --- | --- | --- |
| API `/bin/server` | `API_ADDR=:8080`; `GET /livez` liveness, `GET /readyz` readiness | `DATABASE_URL`의 username `jandibat_api` | `SESSION_SIGNING_KEY`, credential RSA public map과 active ID, identity HMAC active ID와 key map, GitHub/GitLab/Codeberg OAuth client ID와 secret. Private/legacy credential key 및 SMTP secret 금지 |
| Sync worker `/bin/worker` | `WORKER_HEALTH_ADDR=:8081`; `GET /livez`, `GET /readyz` | `WORKER_DATABASE_URL`의 username `jandibat_worker` | credential RSA public/private map과 active ID, 전환 중 필요한 legacy decrypt key, SMTP 네 설정, GitHub/GitLab revoke용 OAuth client ID와 secret. Session, identity HMAC, Codeberg secret 금지 |
| Maintenance `/bin/maintenance` | `MAINTENANCE_HEALTH_ADDR=:8082`; `GET /livez`, `GET /readyz` | `MAINTENANCE_DATABASE_URL`의 username `jandibat_maintenance` | credential RSA public/private map과 active ID, 전환 중 필요한 legacy decrypt key, `DELETION_PSEUDONYM_KEY`, identity HMAC active ID와 key map. Session, SMTP, OAuth secret 금지 |
| Web `/bin/web-start` | `:8080`; `GET /healthz` liveness/readiness | 없음 | Secret 없음. `JANDIBAT_API_BASE_URL`은 공개 HTTPS API origin/path이며 `/tmp/config.json`으로 출력됨 |
| Migration Job `sh /workspace/scripts/db-migrate-url.sh` | Listener/probe 없음; 성공 종료가 완료 신호 | `MIGRATION_DATABASE_URL`의 username `jandibat_migrator`, `COCKROACH_DATABASE=jandibat`, `MIGRATIONS_DIR=/workspace/db/migrations` | 전용 migration credential만. Cockroach CLI, migration SQL, script를 version-pinned Job image/mount로 제공 |

세 Go process에는 `APP_ENV=production`, `BUILD_SHA`, `REGION`, 운영 HTTPS `PUBLIC_BASE_URL`/`WEB_BASE_URL`을 설정합니다. API/worker/maintenance는 각각 자기 DB DSN 하나만 받고 `MIGRATION_DATABASE_URL`은 절대 받지 않습니다. Web에는 `JANDIBAT_API_BASE_URL` 외에 Go process의 secret을 전달하지 않습니다. 표에 없는 비밀의 세부 조건, 예를 들어 worker의 SMTP 네 값 전체와 API의 OAuth 세 provider 전체는 런타임 설정 문서를 따릅니다. DB role별 비밀번호 또는 client certificate는 서로 독립적이어야 합니다.

DB TLS는 CA를 읽기 전용으로 `/var/run/secrets/jandibat/db/ca.crt`에 mount하고 각 DSN의 `sslrootcert`가 그 경로를 가리키게 합니다. 필요하면 client certificate/key도 해당 workload에만 읽기 전용 mount합니다. 공개 HTTPS 호출용 CA bundle은 image의 `/etc/ssl/certs/ca-certificates.crt`에 있으며 `SSL_CERT_FILE`이 이를 가리킵니다. TLS 검증을 끄는 `sslmode=disable`은 격리된 로컬 테스트 전용입니다. Job에는 DB CA와 migration credential만 mount하며 API/worker/maintenance secret을 공유하지 않습니다.

Go process는 read-only root filesystem으로 실행할 수 있습니다. Web은 read-only root filesystem과 numeric UID 101:101로 실행하고 `/tmp`만 writable tmpfs로 mount합니다. 시작 시 `/tmp/config.json`, `/tmp/security-headers.conf`를 만들고 Nginx PID/body/cache도 `/tmp`에 둡니다. Writable `/tmp`가 없으면 시작이 실패해야 합니다. `GET /healthz`는 정적 서버 응답을 검사합니다.

`/livez`는 DB 상태와 독립적인 process liveness입니다. `/readyz`는 DB와 필수 dependency를 검사하며 DB 중단 중에는 503이므로 신규 트래픽이나 작업을 배정하지 않습니다. API `/healthz`는 readiness의 호환 경로입니다. API의 `/metrics`는 실제 TCP peer가 loopback이고 forwarding header가 없을 때만 응답합니다. Prometheus가 Pod IP로 직접 scrape하면 403이므로 같은 Pod 안에서 `127.0.0.1:8080/metrics`를 읽는 sidecar/proxy를 두고 그 proxy의 별도 인증된 endpoint를 scrape합니다. Worker/maintenance health/metrics port는 내부 probe 전용으로 외부에 publish하지 않습니다.

Migration Job은 application rollout보다 먼저 실행합니다. Job image에는 Cockroach `cockroach sql` CLI, `sh`, checksum 도구, 읽기 전용 migration SQL/script가 있어야 하며 임시 transaction file용 writable `/tmp`가 있어야 합니다. `MIGRATION_DATABASE_URL`, `COCKROACH_DATABASE`, `MIGRATIONS_DIR`을 명시하고 URL의 database와 이름이 일치하는지 runner가 확인합니다. Migration 적용 후 같은 credential로 `scripts/db-configure-runtime-roles.sh`를 실행하여 세 runtime role의 GRANT를 갱신합니다. 그 성공과 role negative check를 확인한 뒤 API/worker/maintenance를 시작합니다. 같은 migration을 재실행하면 version/checksum이 일치하는 파일은 `already applied`로 처리하고 checksum 변경은 실패합니다. 실패한 Job은 애플리케이션 rollout을 막습니다.

운영 key 형식은 `DELETED_IDENTITY_HMAC_KEYS`의 각 값이 **정확히 32바이트를 unpadded standard base64**로 인코딩한 JSON map이고, `DELETION_PSEUDONYM_KEY`는 **32바이트 이상을 unpadded base64url**로 인코딩한 값입니다. 두 형식은 바꿔 쓸 수 없습니다. Key 원문이나 DSN을 Job log, image, 배포 manifest에 기록하지 않습니다.

호스트 계약 확인은 `nix develop --command make image-runtime-contract-test`를 사용하며 `make check`에도 포함됩니다. CI migrations job은 격리된 테스트 DB에 migration과 runtime role GRANT를 적용한 뒤 이 검사를 실행합니다. 이때 `TASK3_WORKER_OAUTH_TEST_DSN`에 테스트용 `jandibat_worker` DSN을 제공하여 worker의 DB 연결 이후 OAuth 누락도 실제 process에서 거부되는지 확인합니다. 테스트 key는 실행마다 임시로 생성하고 로그에 출력하지 않습니다. Linux image/container 확인은 `nix develop --command make images-smoke`를 사용합니다. Darwin에서 image output 평가와 Go process 테스트가 통과해도 Linux container smoke를 수행했다고 기록하지 않습니다.
