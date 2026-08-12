# CockroachDB Backup 및 Restore Runbook

기준일: 2026-08-13

대상은 CockroachDB v26.2와 `jandibat` database입니다. RPO는 1시간, RTO는 incident 선언부터 검증된 서비스 복구까지 4시간입니다. 현재 저장소의 로컬 Compose는 backup이 아니며, 이 runbook의 staging restore drill은 아직 실행되지 않았습니다.

공식 문법 기준:

- [CockroachDB v26.2 BACKUP](https://www.cockroachlabs.com/docs/v26.2/backup)
- [CockroachDB v26.2 RESTORE](https://www.cockroachlabs.com/docs/v26.2/restore)
- [CREATE EXTERNAL CONNECTION](https://www.cockroachlabs.com/docs/v26.2/create-external-connection)
- [CHECK EXTERNAL CONNECTION](https://www.cockroachlabs.com/docs/v26.2/check-external-connection)

## 1. Backup 정책

- remote object storage의 dedicated prefix를 external connection `jandibat_backup_v1`로 등록.
- 매일 00:10 UTC full database backup, 매시간 10분 incremental backup.
- `AS OF SYSTEM TIME '-10s'`로 in-flight transaction과의 경계를 둠.
- `revision_history`를 사용해 backup/GC 범위 안의 point-in-time restore를 지원.
- backup collection 보존 35일. object lifecycle은 full/incremental chain을 깨지 않도록 collection 단위로 만료.
- bucket versioning/object lock 또는 provider의 동등한 immutability 기능 활성화.
- storage encryption과 별도 KMS/credential 적용. key와 storage credential은 application secret과 분리.
- 매 backup 후 job success와 `SHOW BACKUP ... WITH check_files`를 검증.
- 매주 schema-only 또는 격리 database restore, 매월 전체 data restore drill.

연속 2회 backup 실패, 마지막 성공 backup이 1시간을 초과, validation 실패는 즉시 page 대상입니다.

## 2. 최소 권한과 external connection 준비

Platform admin이 secure SQL session에서 한 번만 수행합니다. URI에는 credential이 들어갈 수 있으므로 command transcript, shell history, CI output에 남기지 않습니다.

```sql
CREATE ROLE jandibat_backup;
GRANT BACKUP ON DATABASE jandibat TO jandibat_backup;

CREATE EXTERNAL CONNECTION jandibat_backup_v1
AS '<secret-manager가 렌더링한 remote storage URI>';

GRANT USAGE ON EXTERNAL CONNECTION jandibat_backup_v1
TO jandibat_backup;
```

database backup에는 database-level `BACKUP`만 부여합니다. full-cluster `GRANT SYSTEM BACKUP`은 사용하지 않습니다. Restore role은 평시 login을 비활성화하고 drill/incident 때만 short-lived credential로 활성화합니다.

```sql
CREATE ROLE jandibat_restore;
GRANT SYSTEM RESTORE TO jandibat_restore;
GRANT USAGE ON EXTERNAL CONNECTION jandibat_backup_v1
TO jandibat_restore;
```

연결 검증:

```sql
CHECK EXTERNAL CONNECTION 'external://jandibat_backup_v1'
WITH transfer = '32MiB', concurrently = 3, time = '30s';
```

모든 node의 `ok`와 `can_delete`가 true여야 합니다. 결과에는 external URI를 포함하지 않고 node/locality/speed/status만 보관합니다.

## 3. Full 및 incremental backup

수동 full backup:

```sql
BACKUP DATABASE jandibat
INTO 'external://jandibat_backup_v1'
AS OF SYSTEM TIME '-10s'
WITH revision_history;
```

가장 최근 full collection에 incremental 추가:

```sql
BACKUP DATABASE jandibat
INTO LATEST IN 'external://jandibat_backup_v1'
AS OF SYSTEM TIME '-10s'
WITH revision_history;
```

운영 스케줄은 Cockroach scheduled backup 또는 플랫폼 scheduler 중 하나만 authoritative scheduler로 사용합니다. 중복 scheduler를 두지 않습니다. 비동기 실행이 필요하면 `WITH DETACHED`를 사용하고 반환된 job ID가 `succeeded`가 될 때까지 추적합니다.

검증:

```sql
SHOW BACKUPS IN 'external://jandibat_backup_v1';
SHOW BACKUP FROM LATEST IN 'external://jandibat_backup_v1'
WITH check_files;
```

추가 invariant:

- backup end time이 현재 시각에서 1시간 이내.
- `jandibat` database와 모든 expected table이 manifest에 존재.
- file validation error 0건.
- job status `succeeded`, retry 후 orphan running job 0건.
- backup size가 최근 7회 median 대비 50% 이상 급변하면 warning 후 원인 확인.

## 4. Restore drill 사전 조건

- production DB를 대상으로 restore하지 않습니다.
- production과 같은 CockroachDB v26.2 patch level 또는 지원되는 restore target을 사용합니다.
- staging의 synthetic/비식별 data만 drill에 사용합니다.
- 격리 restore database name과 삭제 승인자를 미리 정합니다.
- source backup ID/end time, 예상 RPO, migration checksum 목록을 기록합니다.
- restore role과 external connection access를 drill 시간에만 활성화합니다.
- 기존 database와 같은 이름으로 restore하지 않습니다.

아래 예시는 격리 이름 `jandibat_restore_20260812`를 사용합니다.

## 5. 최신 backup 격리 restore

```sql
RESTORE DATABASE jandibat
FROM LATEST IN 'external://jandibat_backup_v1'
WITH new_db_name = 'jandibat_restore_20260812';
```

blocking SQL session을 오래 유지할 수 없으면 `WITH new_db_name = '...', DETACHED`를 사용하고 job ID를 기록합니다. 최신 backup이 아닌 특정 시점이 필요하면 먼저 `SHOW BACKUPS IN`과 revision window를 확인하고 승인된 timestamp로 restore합니다.

절대 `skip_missing_foreign_keys`, `skip_missing_sequences`, `skip_localities_check`를 정상 drill의 편의 수단으로 사용하지 않습니다. 필요해진 경우 restore를 실패로 판정하고 schema/backup 원인을 조사합니다.

## 6. Restore 검증

RTO 측정은 incident/drill 선언 시각에 시작하고 다음 검증 종료 시 끝납니다.

1. restore job이 `succeeded`인지 확인.
2. `MIGRATION_DATABASE_URL=<대상 URL> make db-migrate-url`을 source와 restore database에 각각 실행해 모든 migration version/checksum이 같고 재실행 시 전부 `already applied`인지 확인.
3. expected table/constraint/index 목록과 row count를 비교.
4. synthetic sentinel user/subject/fact의 referential integrity와 API projection을 확인.
5. 가장 최신 synthetic event의 시각으로 실제 RPO를 계산.
6. restore database를 대상으로 API image를 기동하고 다음 smoke를 수행.

```sh
curl --fail --silent "$RESTORE_API_BASE_URL/healthz"
curl --fail --silent \
  "$RESTORE_API_BASE_URL/v1/activities/$RESTORE_FIXTURE_SUBJECT" \
  | jq -e '.subject == env.RESTORE_FIXTURE_SUBJECT'
curl --fail --silent \
  "$RESTORE_API_BASE_URL/v1/render/$RESTORE_FIXTURE_SUBJECT.svg" \
  | xmllint --noout -
```

7. backup 시각 이후 완료된 account/subject deletion request를 restore database에 replay.
8. deletion verification query가 모두 0인지 확인.
9. audit event chain과 critical mutation count를 reconciliation.
10. RPO 1시간 이하, RTO 4시간 이하인지 판정.

어느 하나라도 실패하면 restore drill은 실패입니다. 일부 smoke 성공을 전체 복구 성공으로 기록하지 않습니다.

저장소의 `scripts/db-restore-verify.sh`는 이 절차를 fail-closed로 묶습니다. 최신 backup end를 기준으로 source를 `AS OF SYSTEM TIME` 조회해 모든 expected table의 exact row count를 복원본과 비교하고, migration checksum·constraint·index inventory와 deletion/mail/HMAC/audit invariant를 검사합니다. 0012 mutation audit outbox도 inventory와 row count에 포함되며 delivered row는 복원된 sink event 전 필드와 일치해야 합니다. 이어서 backup 이후 완료된 deletion manifest를 source에서 일시 export해 복원 DB에 바인딩된 maintenance binary로 replay/잔존 검증하고, 같은 복원 DB에 API를 기동해 fixture activity/SVG smoke와 audit reconciliation을 실행합니다. backup end→검증 시각 RPO와 drill 선언→검증 종료 RTO가 JSON에 기록됩니다.

Staging의 `restore-tools` image는 candidate API image에서 API/maintenance binary를 가져오고 pinned Cockroach client와 verifier를 함께 둡니다. 다음 값은 secret manager의 격리 drill credential/fixture로 실제 설정해야 하며 placeholder나 빈 값이면 `RESTORE_REQUIRE_FULL_DRILL=true` gate가 `NOT RUN`으로 non-zero 종료합니다.

```text
RESTORE_MAINTENANCE_DATABASE_URL_TEMPLATE=postgresql://.../{database}?sslmode=verify-full
RESTORE_API_DATABASE_URL_TEMPLATE=postgresql://.../{database}?sslmode=verify-full
RESTORE_FIXTURE_SUBJECT=restore-fixture
RESTORE_DRILL_STARTED_AT=2026-08-13T00:00:00Z
```

두 URL template은 verifier가 만든 exact 복원 database name으로 치환한 뒤 `current_database()`로 다시 바인딩을 확인합니다. Source/restore/replay credential, manifest와 API log는 artifact에 기록하지 않습니다. 이 자동화가 있어도 repository 안에서는 외부 backup/restore를 실행하지 않았으므로 실제 staging RPO/RTO 상태는 계속 `NOT RUN`입니다.

## 7. 실제 재해 복구와 cutover

1. Incident Commander가 write freeze 또는 maintenance mode를 선언.
2. 장애 cluster에서 더 최신의 일관된 data를 안전하게 보존할 수 있으면 별도 forensic snapshot을 취함.
3. 선택할 backup end time과 예상 data loss를 승인.
4. 새 격리 cluster/database에 restore하고 6절을 전부 실행.
5. production secret은 새 version으로 주입하고 유출 가능성이 있는 token/key는 회전.
6. DNS/load balancer 변경 전에 restored API `/healthz`의 dependency readiness와 10분 synthetic smoke 성공 확인.
7. 5% canary traffic 10분, 25% 10분, 100% 순서로 전환.
8. 각 단계에서 `5xx` 0.1% 초과, p99 2배 초과, decrypt/audit 실패 1건 이상이면 promotion 중단.
9. old cluster는 read-only 격리 상태로 24시간 유지하고 자동 재합류를 금지.

cutover 뒤 양쪽 cluster에 write를 허용하지 않습니다.

## 8. Restore rollback

restore 또는 canary 검증 실패 시:

- 사용자 traffic을 old healthy cluster에 유지하거나 즉시 되돌림.
- restored database는 분석을 위해 read-only로 격리.
- 더 최신 backup 또는 수정된 target cluster로 새 restore를 시작. 실패 database 위에 재restore하지 않음.
- 잘못 적용한 migration을 down migration으로 되감지 않고 호환 가능한 forward fix를 생성.
- 정확한 restore target을 재확인한 뒤에만 실패 database 삭제를 별도 승인.

old cluster도 healthy하지 않다면 rollback이 아니라 다음 valid backup으로 forward recovery합니다. data loss 범위를 숨기지 않고 RPO 위반 incident로 기록합니다.

## 9. Backup storage credential/key 회전

storage credential은 old/new overlap을 확보한 뒤 secure SQL session에서 변경합니다.

```sql
ALTER EXTERNAL CONNECTION jandibat_backup_v1
AS '<secret-manager가 렌더링한 새 remote storage URI>';

CHECK EXTERNAL CONNECTION 'external://jandibat_backup_v1';
```

변경 직후 full backup과 격리 restore를 성공시킨 후 old credential을 폐기합니다. backup encryption key는 35일 보존창의 모든 backup을 decrypt할 수 있을 때까지 archive/decrypt-only 상태로 유지합니다.

## 10. 증거 템플릿

- UTC 시작/종료, 실행자, 승인자, incident/change ID.
- Cockroach exact version, cluster ID의 비민감 식별자, source/target environment.
- external connection 이름. URI와 credential 제외.
- backup/restore job ID, backup end time, file validation 결과.
- source/restore migration version과 checksum.
- table/index/constraint/count comparison artifact.
- deletion replay와 audit reconciliation 결과.
- 실제 RPO/RTO, 목표 대비 pass/fail.
- cutover/rollback 단계와 dashboard 링크.

`docs/evidence/staging/README.md` 양식으로 남기며 실행하지 않은 항목은 `NOT RUN`입니다.
