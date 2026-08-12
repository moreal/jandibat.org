# Custom Provider SDK 가이드

기준 계약: `openapi/jandibat.yaml` 1.1.0

Custom provider는 외부 시스템이 정규화한 activity event를 jandibat API로 밀어 넣는 push-only 통합입니다. API가 고객 endpoint, webhook URL, feed URL을 받아 다시 호출하는 pull/callback 방식이 아닙니다.

## 1. Provider 생성과 key 보관

생성 endpoint는 subject owner의 user session이 필요합니다.

```http
POST /v1/subjects/{subject}/custom-providers
Authorization: Bearer <opaque-user-session-token>
Content-Type: application/json
```

생성 body 제한은 다음과 같습니다.

| field | 필수 | 제한 |
| --- | --- | --- |
| `key` | 예 | 1~64자, `^[a-z0-9][a-z0-9_-]*$` |
| `name` | 예 | 1~100자 |
| `description` | 아니요 | 최대 500자 |
| `allowedActions` | 아니요 | 중복 없는 문자열 최대 100개, 각 1~64자 |

현재 구현은 `allowedActions`를 생략하면 `custom`을 사용합니다. 명시적인 빈 배열은 action을 하나도 허용하지 않으므로 일반적으로 사용하지 않습니다. 현재 ingestion metric은 provider 생성 API에서 설정할 수 없고 `count`만 허용됩니다.

```sh
API_BASE_URL=https://api.example.com
SUBJECT=alice
SESSION_TOKEN='secret-from-your-session-store'

umask 077
curl --fail-with-body \
  --request POST \
  --url "$API_BASE_URL/v1/subjects/$SUBJECT/custom-providers" \
  --header "Authorization: Bearer $SESSION_TOKEN" \
  --header 'Content-Type: application/json' \
  --data '{
    "key": "build_agent",
    "name": "Build Agent",
    "description": "CI build completions",
    "allowedActions": ["build_succeeded", "build_failed"]
  }' \
  --output custom-provider-created.json
```

성공 시 `201 Created`, `Location`, `X-Request-ID`와 다음 형태의 body를 반환합니다.

```json
{
  "provider": {
    "id": "00000000-0000-4000-8000-000000000000",
    "subjectId": "subject-id",
    "environmentId": "custom:build_agent",
    "key": "build_agent",
    "name": "Build Agent",
    "description": "CI build completions",
    "status": "active",
    "allowedActions": ["build_succeeded", "build_failed"],
    "createdAt": "2026-08-12T00:00:00Z",
    "updatedAt": "2026-08-12T00:00:00Z"
  },
  "ingestionKey": "returned-only-once"
}
```

현재 `ingestionKey`는 cryptographic random 32바이트를 padding 없는 base64url로 인코딩한 값입니다. 평문은 생성 응답에서 한 번만 반환됩니다. 응답 파일을 stdout, CI artifact, application log, trace에 남기지 말고 즉시 접근이 제한된 secret manager로 옮깁니다. 서버는 key의 SHA-256 digest만 저장하며 기존 평문을 다시 조회하는 endpoint는 없습니다.

분실 또는 유출 시 다음 owner 전용 endpoint로 교체합니다.

```http
POST /v1/subjects/{subject}/custom-providers/{customProviderId}/rotate-key
Authorization: Bearer <opaque-user-session-token>
```

새 key도 응답에서 한 번만 반환되고, 교체가 성공하는 즉시 이전 key는 인증에 실패합니다. overlap이나 이전 key 복구는 지원하지 않습니다. 현재 rotate 제한은 client address당 시간당 5회입니다.

## 2. Ingestion 계약

```http
POST /v1/custom-providers/{customProviderId}/activities:ingest
X-Jandibat-Provider-Key: <ingestion-key>
Idempotency-Key: <client-generated-key>
Content-Type: application/json
```

`X-Jandibat-Provider-Key`는 custom provider 전용 credential이며 user session이나 `Authorization` bearer token이 아닙니다. `Idempotency-Key`는 8~255자이고 요청마다 client가 생성합니다. 현재 rate limit은 client address와 provider ID 조합당 분당 120회이며 제한 시 `Retry-After`와 `429`를 반환합니다. Production은 CockroachDB bucket으로 API instance 사이에 공유하고, DB 없는 development mode만 process-local fixed window를 사용합니다.

Body의 `schemaVersion`은 문자열 `"1.0"`이어야 합니다.

```json
{
  "schemaVersion": "1.0",
  "events": [
    {
      "eventId": "build:project-a:1042",
      "date": "2026-08-12",
      "action": "build_succeeded",
      "metric": {
        "name": "count",
        "value": 1
      },
      "metadata": {
        "branch": "main",
        "runner": "linux-amd64"
      },
      "observedAt": "2026-08-12T05:14:30Z"
    }
  ]
}
```

### Event와 request 제한

| 항목 | 제한 |
| --- | --- |
| `events` | request당 1~1000개 |
| encoded JSON body | 현재 HTTP reader 최대 1 MiB |
| `eventId` | 필수, provider 안에서 안정적인 dedup ID, 1~255자 |
| `date` | 필수, `YYYY-MM-DD`. 현재 수집 시각(UTC)의 10년 전부터 다음 날까지 |
| `action` | 필수, 1~64자, provider의 `allowedActions` 중 하나 |
| `metric.name` | 필수, 1~64자. 현재 provider는 `count`만 허용 |
| `metric.value` | 필수, `0..1,000,000` 범위의 정수 |
| `metadata` | 선택, 최대 50개 property. key는 1~128자와 `^[A-Za-z0-9._:-]+$`, value는 문자열 최대 1000자 |
| `observedAt` | 선택, RFC 3339 date-time |

OpenAPI의 request/event object는 `additionalProperties: false`이므로 알 수 없는 field는 batch 오류입니다. 현재 JSON decoder도 unknown field와 한 body 안의 여러 JSON value를 거부합니다. 1000개 초과 event와 1 MiB 초과 body는 `413 payload_too_large`로 매핑되므로 두 제한을 모두 넘지 않도록 batch를 더 작게 나눕니다.

JavaScript/TypeScript 예시는 `metric.value`를 정수이면서 `0..1,000,000` 범위로 제한해야 합니다. JSON에 `BigInt`를 직접 넣을 수는 없습니다.

### curl ingestion 예시

```sh
API_BASE_URL=https://api.example.com
CUSTOM_PROVIDER_ID=00000000-0000-4000-8000-000000000000
INGESTION_KEY='secret-from-your-secret-manager'
IDEMPOTENCY_KEY='build-request-20260812-1042'

curl --fail-with-body \
  --request POST \
  --url "$API_BASE_URL/v1/custom-providers/$CUSTOM_PROVIDER_ID/activities:ingest" \
  --header 'Content-Type: application/json' \
  --header "X-Jandibat-Provider-Key: $INGESTION_KEY" \
  --header "Idempotency-Key: $IDEMPOTENCY_KEY" \
  --data '{
    "schemaVersion": "1.0",
    "events": [
      {
        "eventId": "build:project-a:1042",
        "date": "2026-08-12",
        "action": "build_succeeded",
        "metric": {"name": "count", "value": 1},
        "metadata": {"branch": "main"},
        "observedAt": "2026-08-12T05:14:30Z"
      }
    ]
  }'
```

## 3. 성공, duplicate, rejection

요청 envelope, 인증, quota, 크기가 유효하면 event 일부가 거부되더라도 `202 Accepted`입니다.

```json
{
  "accepted": 1,
  "duplicates": 1,
  "rejected": 1,
  "rejections": [
    {
      "eventId": "build:project-a:1044",
      "code": "action_not_allowed",
      "detail": "activity 2: integrations: activity action is not allowed"
    }
  ]
}
```

항상 다음 invariant를 검사합니다.

```text
accepted + duplicates + rejected == submitted events.length
rejections.length == rejected
```

- `accepted`: `(customProviderId, eventId)`가 처음 저장된 유효 event 수.
- `duplicates`: 같은 provider에 이미 저장된 유효 `eventId` 수. 새 `Idempotency-Key`로 보냈더라도 event dedup은 적용됩니다.
- `rejected`: event 단위 validation 실패 수. 같은 batch의 다른 유효 event는 계속 저장됩니다.
- `rejections`: 거부된 event마다 하나이며 batch-level 오류는 포함하지 않습니다.

현재 event rejection code는 `event_id_required`, `event_id_too_long`, `date_required`, `invalid_date`, `date_out_of_range`, `action_required`, `action_too_long`, `action_not_allowed`, `metric_required`, `metric_too_long`, `metric_not_allowed`, `negative_metric_value`, `metric_value_too_large`, `too_many_metadata_properties`, `metadata_value_too_long`입니다. 안정적인 분기는 `code`로 하고 `detail` 문자열을 parsing하지 않습니다.

반면 다음은 전체 batch를 거부하며 `application/problem+json`을 반환합니다.

| status | 대표 조건 |
| --- | --- |
| `400` | JSON/Content-Type/schemaVersion/Idempotency-Key 형식 오류, empty events |
| `401` | provider key 누락 또는 불일치 |
| `404` | custom provider ID 없음 |
| `409` | disabled provider, 같은 Idempotency-Key를 다른 request에 재사용 |
| `413` | event 1000개 초과 또는 encoded JSON body 1 MiB 초과 |
| `429` | rate limit 초과 |
| `500` | 서버 또는 저장소 오류 |

Problem body의 필수 field는 `type`, `title`, `status`, `code`, `requestId`입니다. 지원 요청과 재시도 관찰에는 secret 대신 응답의 `X-Request-ID` 또는 `requestId`를 기록합니다.

## 4. Idempotency와 retry

두 계층의 중복 방지가 함께 동작합니다.

1. `eventId`는 custom provider 범위에서 영구적인 event identity입니다. 같은 사실에는 실행마다 바뀌는 UUID 대신 원본 시스템의 안정적인 ID를 사용합니다.
2. `Idempotency-Key`는 batch request identity이며 최소 24시간 유지됩니다. 같은 provider에서 같은 key와 같은 event body를 재전송하면 최초 `202` 결과를 replay합니다. 같은 key를 다른 body에 재사용하면 `409 Conflict`입니다.

Network timeout, 연결 종료, `429`, 일시적인 `5xx` 뒤에는 body를 바꾸지 않고 같은 `Idempotency-Key`로 재시도합니다. `429`에는 `Retry-After`를 우선하고 exponential backoff와 jitter를 적용합니다. Event를 추가·제거·수정했다면 새 key를 사용합니다. `4xx` validation/auth 오류는 입력이나 credential을 고치기 전 재시도하지 않습니다.

Production은 DB가 필수이고 idempotency record가 CockroachDB에 저장됩니다. DB 없이 실행하는 development mode의 fallback store는 process memory이므로 process 재시작을 가로질러 replay 결과를 유지하지 못합니다.

## 5. TypeScript SDK

Workspace package `@jandibat/custom-provider-sdk`는 계약 타입, 입력 검증, 고정 ingestion endpoint, 응답 partition 검증과 비밀값을 포함하지 않는 typed error를 제공합니다.

```sh
yarn add @jandibat/custom-provider-sdk
```

`apiBaseUrl`은 credential/query/fragment가 없는 HTTPS URL만 허용하고, `providerId`는 생성 응답의 UUID여야 합니다. SDK는 임의 endpoint override를 받지 않고 `/v1/custom-providers/{providerId}/activities:ingest`만 호출합니다. `ingestionKey`는 한 번만 반환된 값을 secret manager에서 주입하며 client를 serialize해도 노출되지 않습니다.

```ts
import {
  createCustomProviderClient,
  createIdempotencyKey,
  CustomProviderApiError,
} from "@jandibat/custom-provider-sdk";

const client = createCustomProviderClient({
  apiBaseUrl: "https://api.example.com",
  providerId: process.env.JANDIBAT_CUSTOM_PROVIDER_ID!,
  ingestionKey: process.env.JANDIBAT_CUSTOM_PROVIDER_KEY!,
});

// 실제 job/request record에 먼저 저장하고, 결과가 불명확한 재시도에서도
// 같은 event body와 같은 key를 사용합니다.
const idempotencyKey = await loadPersistedKey() ?? createIdempotencyKey();
await persistKeyBeforeSending(idempotencyKey);

try {
  const result = await client.push([{
    eventId: "build:project-a:1042",
    date: "2026-08-12",
    action: "build_succeeded",
    metric: { name: "count", value: 1 },
    metadata: { branch: "main" },
  }], { idempotencyKey });
  console.info("custom ingest", {
    accepted: result.accepted,
    duplicates: result.duplicates,
    rejected: result.rejected,
  });
} catch (error) {
  if (error instanceof CustomProviderApiError) {
    console.error("custom ingest failed", {
      status: error.status,
      code: error.code,
      requestId: error.requestId,
      retryAfter: error.retryAfter,
    });
  }
  throw error;
}
```

Error message나 log에는 `ingestionKey`, request body 또는 upstream problem detail을 넣지 않습니다. `CustomProviderApiError`의 stable `status`/`code`/`requestId`/`retryAfter`, validation issue의 field path만 기록합니다. 새 body를 보낼 때는 새 idempotency key를 생성합니다.

### Dependency-free 참고 구현

아래 예시는 SDK를 설치할 수 없는 환경에서 Web/Node 표준 `fetch`로 같은 규약을 직접 구현할 때만 사용합니다.

```ts
type CustomActivityEvent = {
  eventId: string;
  date: string;
  action: string;
  metric: { name: "count"; value: number };
  metadata?: Record<string, string>;
  observedAt?: string;
};

type IngestResult = {
  accepted: number;
  duplicates: number;
  rejected: number;
  rejections: Array<{ eventId: string; code: string; detail: string }>;
};

type Problem = {
  type: string;
  title: string;
  status: number;
  code: string;
  requestId: string;
  detail?: string;
};

export async function ingestActivities(input: {
  apiBaseUrl: string;
  customProviderId: string;
  ingestionKey: string;
  events: CustomActivityEvent[];
  idempotencyKey: string;
}): Promise<IngestResult> {
  if (input.events.length < 1 || input.events.length > 1000) {
    throw new RangeError("events must contain 1 to 1000 items");
  }
  if (input.idempotencyKey.length < 8 || input.idempotencyKey.length > 255) {
    throw new RangeError("idempotencyKey must contain 8 to 255 characters");
  }
  for (const event of input.events) {
    if (!Number.isSafeInteger(event.metric.value) || event.metric.value < 0 || event.metric.value > 1_000_000) {
      throw new RangeError("metric.value must be an integer between 0 and 1,000,000");
    }
  }
  const endpoint = new URL(
    `/v1/custom-providers/${encodeURIComponent(input.customProviderId)}/activities:ingest`,
    input.apiBaseUrl,
  );
  const body = JSON.stringify({ schemaVersion: "1.0", events: input.events });

  const response = await fetch(endpoint, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-Jandibat-Provider-Key": input.ingestionKey,
      "Idempotency-Key": input.idempotencyKey,
    },
    body,
  });

  if (!response.ok) {
    const problem = (await response.json()) as Problem;
    throw new Error(
      `custom ingest failed: ${problem.status} ${problem.code} requestId=${problem.requestId}`,
    );
  }

  const result = (await response.json()) as IngestResult;
  if (
    result.accepted + result.duplicates + result.rejected !== input.events.length ||
    result.rejections.length !== result.rejected
  ) {
    throw new Error("custom ingest response count invariant failed");
  }
  return result;
}
```

`ingestionKey`, request header, full error object를 log하지 않습니다. 호출부는 전송 전에 `crypto.randomUUID()` 같은 방식으로 `idempotencyKey`를 만들고 non-secret job state에 저장한 뒤 이 함수에 전달합니다. 동일 작업의 재시도에는 저장한 key와 같은 event body를 다시 사용합니다.

## 6. Push-only와 SSRF 경계

Custom provider 생성 schema와 ingestion schema에는 URL field가 없습니다. 서버는 custom provider 요청을 계기로 사용자가 지정한 host로 fetch하거나 redirect를 따라가지 않습니다. 따라서 이 통합 경로에는 임의 URL을 서버가 조회하게 만드는 SSRF 입력 surface가 없습니다.

이 속성은 custom provider 프로토콜의 서버 측 경계에 한정됩니다. SDK를 호출하기 전에 별도 client code가 임의 URL에서 데이터를 수집한다면 그 client의 allowlist, DNS/IP 검증, redirect 정책은 별도로 적용해야 합니다.
