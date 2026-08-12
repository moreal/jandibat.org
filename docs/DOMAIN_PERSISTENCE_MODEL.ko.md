# 도메인 저장/조회 모델

최종 갱신일: 2026-08-12

## 목적

CockroachDB 구현과 무관하게 activity 도메인의 저장/조회 계약과 순수 로직을 고정합니다. 운영 adapter는 `apps/api/internal/adapters/**/cockroach`에 있고 schema의 단일 진실 원천은 `db/migrations/*.sql`입니다.

## 핵심 규약

- 도메인 로직은 순수 함수로 작성한다.
- 부수 효과(HTTP, DB, 외부 API 호출)는 도메인 밖(Application/Adapter)에서 처리한다.
- 저장 계층은 교체 가능해야 하므로 도메인은 Port만 의존한다.

## 도메인 구성

`apps/api/internal/domain/activity/model.go`

- `Environment`: 독립 엔터티(별도 테이블 대상)
  - `id`, `key`, `name`, `scope(global|subject)`, `owner_subject`, `metadata`
  - GitHub/GitLab/Codeberg 외에도 사용자/외부 서비스 환경을 임의로 추가 가능
- `Fact`: 원자 활동 레코드
  - `subject`, `date`, `environment_id`, `action`, `metric`, `metadata(map[string]string)`
  - 예: GitHub repo `jandibat.org`에서 commit `10`
- `Timeline`: heatmap 렌더링용 projection 결과
  - `days[]` 내부에 집계 count/level + 세부 `entries[]`를 포함
  - `environments[]`로 entry의 `environment_id`를 해석 가능

즉, 저장의 기본 단위는 `Environment + Fact`이고, `Timeline`은 이 둘을 join해서 만든 캐시/뷰 모델입니다.

## 저장/조회 포트

`apps/api/internal/domain/activity/ports.go`

- `SaveFacts(input)`
  - 의미: subject의 활동 사실(Fact)들을 저장
- `LoadFacts(input)`
  - 의미: 기간 조건(`from`, `to`)으로 Fact 조회
- `SaveEnvironments(input)`
  - 의미: 환경 정의(기본/사용자정의)를 저장
- `LoadEnvironments(input)`
  - 의미: environment id 목록으로 환경 정의 조회

이 포트를 기준으로 메모리/Cockroach/기타 저장소 구현체를 교체합니다.

## 순수 도메인 로직

`apps/api/internal/domain/activity/logic.go`

- `BuildTimelineFromFacts(subject, timezone, environments, facts)`
  - Fact 검증
  - environment id join 검증(unknown id 차단)
  - 일자별 집계
  - heatmap level 계산
  - 렌더링용 entry(metadata 포함) + environments 카탈로그 유지
- `LevelForCount(count)`
  - count -> level(0..4) 변환

## 캐시/갱신 정책 (도메인 규약)

모든 데이터는 캐시 관점으로 다룹니다.

- 오늘(`hot`) 데이터: 짧은 TTL로 자주 갱신
- 과거(`cold`) 데이터: 긴 TTL 또는 `0`(반영구)로 유지
- 필요 시 강제 refresh 허용(오늘이 아니어도 가능)
- 캐시 payload에는 `payload_schema_version`을 포함해 스키마 변경 시 안전하게 무효화/재생성
- 외부 fetch 실패 시 정책 선택
  - `keep_stale`: 기존 데이터 유지
  - `purge`: 기존 데이터 제거

관련 순수 함수:

- `ShouldRefresh(...)`
- `TTLForDate(...)`
- `ResolveFactsOnFetchFailure(...)`

## 계층 경계

- Domain: 모델, 검증, 집계, 캐시 의사결정(순수)
- Application: fetch-or-cache 유스케이스 조합(Port + 외부 클라이언트 호출 orchestration)
- Adapter: chi handler, provider API 클라이언트, 저장소 구현체

## 구현체와 검증

- memory adapter: 순수 도메인·HTTP 단위 테스트와 로컬 fallback
- Cockroach adapter: activity, auth, subject, integration, operations 저장
- provider adapter: GitHub/GitLab/Codeberg 수집과 공통 실패 분류
- 검증: `make test-api`, `make db-up db-migrate`, 이후 같은 migration을 한 번 더 적용해 checksum/idempotency 확인
