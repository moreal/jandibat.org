# 구현 블루프린트 (Multi-agent + ERD)

최종 갱신일: 2026-08-12

> 역사적 구현 초안입니다. 아래 OpenAPI 단일 계약과 `GET /v1/activities/{subject}` 시퀀스는
> 2026-09-22 플랫폼 현대화에서 대체됐으며 현재 실행 지침이 아닙니다. 현행 도메인 계약은
> [`graphql/schema/`](../graphql/schema/)의 SDL, HTTP edge 계약은
> [`openapi/jandibat.yaml`](../openapi/jandibat.yaml)을 참조하세요. 진행 상태는
> [`MODERNIZATION_BACKLOG.ko.md`](MODERNIZATION_BACKLOG.ko.md)를 따릅니다.

## 1) 멀티에이전트 구현 순서도

```mermaid
flowchart TD
  A[Coordination: Domain 규약 확정] --> B[Coordination: ERD / OpenAPI 고정]
  B --> C1[Backend: Domain/Application 유스케이스 구현]
  B --> C2[Frontend: OpenAPI 타입 생성/화면 스켈레톤]
  C1 --> D1[Backend: Adapter 구현
  - provider client
  - storage adapter]
  C2 --> D2[Frontend: Environment별 렌더링 구현]
  D1 --> E[Coordination: 계약 정합성 검증]
  D2 --> E
  E --> F[병합 + 배포]
```

## 2) 요청 처리 시퀀스 (캐시/강제갱신/실패정책)

```mermaid
sequenceDiagram
  autonumber
  participant U as User
  participant API as API Handler
  participant APP as Activity Usecase
  participant S as ActivityStore
  participant P as Provider Client
  participant D as Domain Logic

  U->>API: GET /v1/activities/{subject}?date=2026-03-02&force=false
  API->>APP: GetTimeline(subject, date, force, failure_policy)
  APP->>S: LoadFacts(subject, date)
  APP->>S: LoadEnvironments(environment_ids)
  APP->>D: ShouldRefresh(now, cachedAt, targetDate, today, policy, force)

  alt refresh 필요
    APP->>P: Fetch latest facts
    alt fetch 성공
      APP->>S: SaveFacts(newFacts)
      APP->>S: SaveEnvironments(newOrUpdatedEnvironments)
    else fetch 실패 + keep_stale
      APP->>D: ResolveFactsOnFetchFailure(existing, keep_stale)
    else fetch 실패 + purge
      APP->>D: ResolveFactsOnFetchFailure(existing, purge)
      APP->>S: SaveFacts(empty)
    end
  else refresh 불필요
    Note over APP: 기존 캐시 facts 사용
  end

  APP->>D: BuildTimelineFromFacts(subject, timezone, environments, facts)
  D-->>APP: Timeline
  APP-->>API: Timeline
  API-->>U: JSON response
```

## 3) 개념 ERD

```mermaid
erDiagram
  USERS ||--o{ SUBJECTS : owns
  USERS ||--o{ USER_PASSKEYS : has
  USERS ||--o{ MAGIC_LINK_TOKENS : issues
  SUBJECTS ||--o{ ENVIRONMENTS : owns_if_subject_scope
  SUBJECTS ||--o{ ACTIVITY_FACTS : has
  ENVIRONMENTS ||--o{ ACTIVITY_FACTS : referenced_by
  SUBJECTS ||--o{ PROVIDER_CONNECTIONS : has
  ENVIRONMENTS ||--o{ PROVIDER_CONNECTIONS : connects_to
  SUBJECTS ||--o{ TIMELINE_CACHE : has

  USERS {
    string id PK
    string primary_email
    string status
  }

  USER_PASSKEYS {
    uuid id PK
    string user_id
  }

  MAGIC_LINK_TOKENS {
    uuid id PK
    string user_id
  }

  SUBJECTS {
    string id PK
    string owner_user_id FK
    string handle
    string timezone
  }

  ENVIRONMENTS {
    string id PK
    string key
    string name
    string scope
    string owner_subject_id FK
    json metadata
  }

  PROVIDER_CONNECTIONS {
    uuid id PK
    string subject_id FK
    string environment_id FK
    string auth_method
    string status
  }

  ACTIVITY_FACTS {
    uuid id PK
    string subject_id FK
    string environment_id FK
    uuid provider_connection_id FK
    date activity_date
    string action
    string metric_name
    int metric_value
    json metadata
    timestamp observed_at
  }

  TIMELINE_CACHE {
    string id PK
    string subject_id FK
    date target_date
    int payload_schema_version
    timestamp fetched_at
    timestamp expires_at
    string failure_policy
  }
```

상세 버전(개념 + 물리 초안)은 `docs/ERD_CONCEPTUAL_AND_PHYSICAL.ko.md`를 기준으로 봅니다.

## 4) 에이전트별 산출물 경계

- Coordination Agent
  - `docs/**`, `openapi/**`, CI, 인터페이스 변경 로그
- Backend Agent
  - `apps/api/internal/domain/**`
  - `apps/api/internal/application/**`
  - `apps/api/internal/adapters/**`
- Frontend Agent
  - `apps/web/**`, `packages/contracts/**`

## 5) 구현 우선순위

1. Domain/ERD/OpenAPI 고정
2. Backend: `GetTimeline` 유스케이스(도메인 순수 로직 + 포트 조합)
3. Frontend: environment-aware timeline 렌더링
4. Adapter(Storage/Provider) 연결
5. 계약/회귀 테스트 자동화
