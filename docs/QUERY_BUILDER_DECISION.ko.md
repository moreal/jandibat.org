# Go SQL 접근 방식 의사결정

> 상태: 2026-09-22에 [`PLATFORM_MODERNIZATION_DESIGN.ko.md`](PLATFORM_MODERNIZATION_DESIGN.ko.md)로
> 대체됨. 아래 내용은 과거 의사결정 기록이다. 새 구현은 공식 원본 Scythe의 CockroachDB +
> Go pgx 지원을 먼저 검증하고, 확인된 결함에만 저장소 로컬 Nix patch를 적용한다.

## 2026-09-23 구현 진행 기록

- 단일 baseline `db/migrations/0001_baseline.sql`과 공식 Scythe v0.17.0의
  `cockroachdb` / `go-pgx` 엔진을 기준으로 한다. 정적 SQL은 Scythe가 생성한
  pgx 함수로 전환하며, 동적 식별자는 별도 allowlist를 적용한다.
- 초기 13개 migration은 `0001_baseline.sql` 하나로 교체해 빈 DB에서 검증했다.
  이후 예약 소유권 경쟁을 막기 위해 `0002_ingest_reservation_token.sql`을
  추가했다. 이는 폐기 대상인 기존 history의 연장이 아니라 새 baseline 이후의
  additive 변경이다. 이미 적용한 격리 DB의 baseline을 다시 쓰거나 DB를
  삭제하지 않으며, migration 테스트는 두 새 버전과 legacy history 거부를 검증한다.
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
- `scytheprobe`는 운영 쿼리로 대체하지 않는다. 최소 fixture가 `UPSERT`,
  Cockroach catalog 정수 폭, nullable JSONB/배열, pool/Tx 겸용 생성 시그니처를
  직접 회귀 검증하므로 upstream local patch의 제거 조건에 필요하다.
- 정적 쿼리 계약은 `scythe.toml`의 각 `.sql` 파일이다. `make sql-generate`로
  Go 코드를 재생성하고, `make sql-check sql-generated-drift-check`로 쿼리 의미와
  체크인된 생성물의 일치를 각각 검사한다. drift 검사는 임시 복사본만 생성하므로
  작업 트리의 생성 파일을 덮어쓰지 않는다. CI backend job은 이 두 offline 검사를,
  migration job은 baseline 적용 뒤 `make sql-live-drift-test sql-check-live`를
  실행한다. 두 drift 테스트는 복사본의 생성 Go 파일 또는 schema 열을 바꿔
  offline/live 게이트가 각각 실패하는지 확인한다.
- 런타임의 불가피한 동적 retention SQL은 닫힌 dataset→테이블/열 매핑에서만
  식별자를 선택하며 cutoff·limit·as-of 값은 바인딩한다. 식별자 거부는 fuzz
  테스트로 검사한다. SQLSTATE 40001 retry는 Scythe가 아니라 pgx 트랜잭션 경계가 담당한다.
- 해당 보정이 포함된 공식 Scythe release가 나오고 위 Nix check 및 실제 DB
  검증이 원본에서 통과할 때 local patch를 제거한다. upstream issue/PR은 아직
  작성하지 않았다. 외부 저장소에 쓰기 전에 별도 조율이 필요하다.

## 2026-09-24 운영 Go 수기 SQL 허용 목록

`make adapter-sql-allowlist-check`는 Go AST에서 generated 파일과 테스트를 제외한
문자열 리터럴을 검사한다. 네 예외 파일에는 근거 주석과 전체 문자열 리터럴의
SHA-256 manifest를 요구하므로 기존 SQL을 같은 줄에서 바꾸거나 새 문자열을
추가해도 검토 없이 통과하지 않는다. 다른 adapter 파일에서는 대소문자와 개행에
무관하게 일반적인 SQL 문장 형태를 거부하며, 동일 파일의 전역·함수 로컬
`const` 식별자를 어휘 범위대로 해석한 정적 `+` 연결과 SQL 블록·줄 주석도
검사한다. 다른 파일의 `const`
참조나 `fmt.Sprintf`처럼 문자열을 동적으로 조립한 모든 SQL을 의미론적으로
증명하지는 못하므로 코드 리뷰와 실제 DB 테스트를 대체하지 않는다.

- `integrations/cockroach/revocations.go`, `operations/cockroach/store.go`,
  `operations/cockroach/audit_outbox.go`의 각 1개 조회: 고정된
  `information_schema.columns` readiness 검사다. 값/식별자를 요청에서 조합하지
  않고, 활성 pgx 트랜잭션을 따른다. 공식 무패치 Scythe 0.17.0과 현재 Nix 패치
  바이너리는 이 Cockroach 가상 카탈로그를 `UNKNOWN_TABLE`로 거부하지만,
  Cockroach 26.2.5는 같은 쿼리를 실행한다. Scythe의 정적 스키마 모델에 없는
  기능이지 확인된 upstream 결함은 아니므로 이 세 조회 때문에 local patch를
  늘리지 않는다. 앱과 무관한 최소 재현은
  `nix/fixtures/scythe-cockroach-repro/virtual-catalog-scythe.toml`에 있다.
  해당 디렉터리에서 `scythe check --config virtual-catalog-scythe.toml`을 실행하면
  현재 고정된 Scythe 0.17.0에서 `SC-PARSE02 UNKNOWN_TABLE:
  relation "information_schema.columns" does not exist`로 종료 코드 2를 반환한다.
  실제 DB의 전체/누락 열과 트랜잭션 경계 테스트를 유지한다.
- `operations/cockroach/retention.go`의 동적 SQL: 내부
  `RetentionDataset`에서 닫힌 테이블·열 spec만 선택하는 동적 식별자 SQL이다.
  cutoff·limit·as-of는 바인딩하고 미등록 dataset은 실행 전에 거부한다.
  법적 보존 조건, 유지보수 역할, bounded fuzz를 검사한다.

설명 주석은 AST 문자열 인벤토리에 포함하지 않는다. catalog 가상 테이블의
생성 지원이 공식 Scythe에 추가되면 위 3개 예외를 생성 쿼리로 옮기고 허용
목록을 줄인다.

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
4. production URL 기반 적용은 `MIGRATION_DATABASE_URL=... COCKROACH_DATABASE=jandibat MIGRATIONS_DIR="$PWD/db/migrations" TMPDIR=/tmp make db-migrate-url`이며 Cockroach CLI와 쓰기 가능한 임시 디렉터리가 필요합니다. `DATABASE_URL` fallback은 없습니다.

## 참고 링크

- Jet: https://github.com/go-jet/jet
- goqu: https://github.com/doug-martin/goqu
- Squirrel: https://github.com/Masterminds/squirrel
- Bun: https://bun.uptrace.dev/
- Cockroach + Go(pg x): https://www.cockroachlabs.com/docs/stable/build-a-go-app-with-cockroachdb-pgx
