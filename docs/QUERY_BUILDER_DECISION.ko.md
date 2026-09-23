# Go SQL 접근 방식 의사결정

> 상태: 2026-09-22에 [`PLATFORM_MODERNIZATION_DESIGN.ko.md`](PLATFORM_MODERNIZATION_DESIGN.ko.md)로
> 대체됨. 아래 내용은 과거 의사결정 기록이다. 새 구현은 공식 원본 Scythe의 CockroachDB +
> Go pgx 지원을 먼저 검증하고, 확인된 결함에만 저장소 로컬 Nix patch를 적용한다.

## 2026-09-23 구현 진행 기록

- 단일 baseline `db/migrations/0001_baseline.sql`과 공식 Scythe v0.17.0의
  `cockroachdb` / `go-pgx` 엔진을 기준으로 한다. 정적 SQL은 Scythe가 생성한
  pgx 함수로 전환하며, 동적 식별자는 별도 allowlist를 적용한다.
- 공식 원본 v0.9.0과 v0.17.0 모두 최소 `UPSERT` fixture를 파싱하지 못했다.
  v0.17.0의 live check는 최소 단일 테이블 CockroachDB에서
  `pg_class/schema_rank`를 int4로 읽다가 panic했다. 원본 Go 생성 함수는
  `*pgxpool.Pool`만 받아 트랜잭션 안에서 호출할 수 없었다.
- 이 세 결함만 `nix/patches/scythe-cockroach.patch`에서 보정한다. 파서용 SQL만
  `UPSERT`를 `INSERT`로 해석하고 실행 SQL은 보존하며, catalog rank의 int4/int8을
  모두 허용하고, 생성 함수는 pool과 `pgx.Tx`가 구현하는 `DBTX`를 받는다.
- `nix/fixtures/scythe-cockroach-repro/`는 앱 코드가 없는 최소 재현이고,
  `nix build .#checks.<system>.scythe-compatibility`가 생성 결과를 강제한다.
  실제 DB 검증은 `make sql-check-live`와 Go integration test로 수행한다.
- 해당 보정이 포함된 공식 Scythe release가 나오고 위 Nix check 및 실제 DB
  검증이 원본에서 통과할 때 local patch를 제거한다. upstream issue/PR은 아직
  작성하지 않았다. 외부 저장소에 쓰기 전에 별도 조율이 필요하다.

아래 의사결정은 이전 `database/sql` 구현의 기록이며 현재 전환 목표가 아니다.

최종 갱신일: 2026-08-12

## 배경

- 백엔드는 Go + chi로 구현
- CockroachDB를 운영 데이터베이스로 확정
- 도메인·애플리케이션은 저장 port에만 의존하고 SQL은 adapter에 격리

## 조사 후보

1. `go-jet/jet`
- SQL Builder + Code Generation 방식
- README에 PostgreSQL, MySQL, MariaDB, SQLite, CockroachDB 테스트 지원 명시
- 장점: 타입 안정성, 복잡한 쿼리에서 컴파일 단계 오류 탐지
- 단점: 초기 셋업 및 코드젠 파이프라인 필요

2. `doug-martin/goqu`
- SQL builder 중심 라이브러리
- PostgreSQL/MySQL/SQLite3 등 dialect 지원
- 장점: 동적 필터 조합이 편하고 ORM 강제성이 약함
- 단점: 스키마 기반 타입 안정성은 Jet 대비 약함

3. `Masterminds/squirrel`
- SQL query builder
- README에 "complete" 상태(신규 기능보다 버그픽스 중심) 명시
- 장점: 사용법 단순
- 단점: 장기 확장성/활성 개발 관점에서 신규 선택으로는 보수적

4. `uptrace/bun`
- SQL-first ORM + query builder
- PostgreSQL/MySQL/SQLite/MSSQL 지원
- 장점: 모델링/마이그레이션 포함한 통합 개발 경험
- 단점: 프로젝트에 ORM 계층 도입 여부를 먼저 결정해야 함

## 의사결정

- CockroachDB 연결은 `github.com/jackc/pgx/v5/stdlib`과 `database/sql`을 사용합니다.
- 현재 쿼리는 작은 adapter별 명시적 SQL로 유지합니다. Jet/goqu/Squirrel/Bun은 도입하지 않습니다.
- transaction, row lock/CAS, Cockroach 오류 분류를 숨기지 않고 repository 구현에서 직접 다룹니다.
- schema의 단일 진실 원천은 `db/migrations/*.sql`이며, migration 파일 checksum은 적용 뒤 변경할 수 없습니다.
- 반복 SQL이 유지보수 한계를 넘을 때만 별도 ADR과 측정 결과를 근거로 builder/codegen 도입을 재검토합니다.

선정 이유는 현재 쿼리와 Cockroach 고유 동시성 처리를 가장 투명하게 검토할 수 있고, 생성 코드 drift나 ORM 수명주기를 추가하지 않으면서 port/adapter 경계를 지킬 수 있기 때문입니다.

## 검증 경로

1. `make test-api`: repository의 scripted SQL/CAS/transaction 테스트
2. `make db-up db-migrate`: 실제 CockroachDB에 전체 migration 적용
3. 별도 명령으로 `make db-migrate`를 다시 실행해 idempotency와 checksum 확인
4. production URL 기반 적용은 `DATABASE_URL=... make db-migrate-url`

## 참고 링크

- Jet: https://github.com/go-jet/jet
- goqu: https://github.com/doug-martin/goqu
- Squirrel: https://github.com/Masterminds/squirrel
- Bun: https://bun.uptrace.dev/
- Cockroach + Go(pg x): https://www.cockroachlabs.com/docs/stable/build-a-go-app-with-cockroachdb-pgx
