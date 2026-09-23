# 로컬 CockroachDB 실행 가이드 (Docker Compose)

v26.2 개발 데이터는 Compose 프로젝트별 `cockroach-data-v26` 볼륨에 저장합니다. 2026-09-22 플랫폼 현대화 이후 `db/migrations/0001_baseline.sql` 하나가 새 스키마의 출발점입니다. 기존 0001–0013 migration history와 데이터의 업그레이드·복사 경로는 제공하지 않습니다. Migrator는 옛 history나 관리되지 않은 테이블이 있는 DB를 거부합니다.

기존 로컬 DB/볼륨을 삭제하려면 먼저 `docker compose config --format json`에서 프로젝트와 실제 볼륨 이름을 확인하고, 해당 DB의 데이터가 더 필요 없는지 사용자 승인을 받으세요. 이 가이드는 기존 볼륨을 자동으로 삭제하지 않습니다. 새 Compose 프로젝트의 전용 볼륨이나 완전히 비어 있는 새 DB에 baseline을 적용할 수 있습니다.

## 1) DB 실행

```bash
make db-up
```

- SQL 포트: `localhost:26257`
- DB 콘솔(Admin UI): `http://localhost:18080`
- 기본 DB: `jandibat`

## 2) 스키마 적용

```bash
make db-migrate
```

새 테스트 DB의 최종 스키마와 단일 migration 기록은 다음 명령으로 검증합니다.

```bash
sh scripts/test-baseline-schema.sh
sh scripts/test-baseline-history-rejection.sh
```

## 3) SQL 쉘 접속

```bash
make db-shell
```

쉘 접속 후:

```sql
USE jandibat;
```

## 4) 종료

```bash
make db-down
```
