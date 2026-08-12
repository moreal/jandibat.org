import type {
  CustomActivityEventDto,
  CustomActivityIngestDto,
} from "@jandibat/contracts";
import {
  CustomProviderValidationError,
  type CustomProviderValidationIssue,
} from "./errors.ts";

export const CUSTOM_PROVIDER_SCHEMA_VERSION = "1.0" as const;
export const MAX_CUSTOM_PROVIDER_BATCH = 1_000;
export const MAX_CUSTOM_PROVIDER_PAYLOAD_BYTES = 1 << 20;

const MAX_EVENT_ID_LENGTH = 255;
const MAX_NAME_LENGTH = 64;
const MAX_METRIC_VALUE = 1_000_000;
const MAX_METADATA_PROPERTIES = 50;
const MAX_METADATA_KEY_LENGTH = 128;
const MAX_METADATA_VALUE_LENGTH = 1_000;
const MAX_BACKFILL_YEARS = 10;
const MAX_FUTURE_DAYS = 1;
const DATE_PATTERN = /^(\d{4})-(\d{2})-(\d{2})$/;
const DATE_TIME_PATTERN = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;
const METADATA_KEY_PATTERN = /^[A-Za-z0-9._:-]+$/;

function runeLength(value: string): number {
  return Array.from(value).length;
}

function issue(
  issues: CustomProviderValidationIssue[],
  path: string,
  code: string,
  message: string,
): void {
  issues.push({ path, code, message });
}

function validDate(value: string): Date | undefined {
  const match = DATE_PATTERN.exec(value);
  if (!match) return undefined;
  const date = new Date(`${value}T00:00:00.000Z`);
  if (Number.isNaN(date.getTime()) || date.toISOString().slice(0, 10) !== value) {
    return undefined;
  }
  return date;
}

function dateBounds(now: Date): { earliest: Date; latest: Date } {
  const today = new Date(Date.UTC(
    now.getUTCFullYear(),
    now.getUTCMonth(),
    now.getUTCDate(),
  ));
  const earliest = new Date(today);
  earliest.setUTCFullYear(earliest.getUTCFullYear() - MAX_BACKFILL_YEARS);
  const latest = new Date(today);
  latest.setUTCDate(latest.getUTCDate() + MAX_FUTURE_DAYS);
  return { earliest, latest };
}

function validateString(
  value: unknown,
  path: string,
  maximum: number,
  issues: CustomProviderValidationIssue[],
): value is string {
  if (typeof value !== "string" || value.length === 0) {
    issue(issues, path, "required", "비어 있지 않은 문자열이어야 합니다.");
    return false;
  }
  if (runeLength(value) > maximum) {
    issue(issues, path, "too_long", `${maximum}자 이하여야 합니다.`);
    return false;
  }
  return true;
}

function validateEvent(
  input: unknown,
  index: number,
  now: Date,
  issues: CustomProviderValidationIssue[],
): CustomActivityEventDto | undefined {
  const path = `/events/${index}`;
  if (typeof input !== "object" || input === null || Array.isArray(input)) {
    issue(issues, path, "invalid_type", "객체여야 합니다.");
    return undefined;
  }
  const value = input as Record<string, unknown>;
  const eventIdValid = validateString(
    value.eventId,
    `${path}/eventId`,
    MAX_EVENT_ID_LENGTH,
    issues,
  );
  const actionValid = validateString(
    value.action,
    `${path}/action`,
    MAX_NAME_LENGTH,
    issues,
  );

  let date: Date | undefined;
  if (typeof value.date !== "string" || !(date = validDate(value.date))) {
    issue(issues, `${path}/date`, "invalid_date", "YYYY-MM-DD 형식의 실제 날짜여야 합니다.");
  } else {
    const { earliest, latest } = dateBounds(now);
    if (date < earliest || date > latest) {
      issue(
        issues,
        `${path}/date`,
        "date_out_of_range",
        "최근 10년부터 내일까지의 날짜만 사용할 수 있습니다.",
      );
    }
  }

  const metric = value.metric;
  let metricNameValid = false;
  let metricValueValid = false;
  if (typeof metric !== "object" || metric === null || Array.isArray(metric)) {
    issue(issues, `${path}/metric`, "invalid_type", "객체여야 합니다.");
  } else {
    const candidate = metric as Record<string, unknown>;
    metricNameValid = validateString(
      candidate.name,
      `${path}/metric/name`,
      MAX_NAME_LENGTH,
      issues,
    );
    metricValueValid =
      typeof candidate.value === "number" &&
      Number.isInteger(candidate.value) &&
      candidate.value >= 0 &&
      candidate.value <= MAX_METRIC_VALUE;
    if (!metricValueValid) {
      issue(
        issues,
        `${path}/metric/value`,
        "out_of_range",
        `0부터 ${MAX_METRIC_VALUE} 사이의 정수여야 합니다.`,
      );
    }
  }

  let metadata: Record<string, string> | undefined;
  if (value.metadata !== undefined) {
    if (
      typeof value.metadata !== "object" ||
      value.metadata === null ||
      Array.isArray(value.metadata)
    ) {
      issue(issues, `${path}/metadata`, "invalid_type", "문자열 값 객체여야 합니다.");
    } else {
      const entries = Object.entries(value.metadata);
      if (entries.length > MAX_METADATA_PROPERTIES) {
        issue(
          issues,
          `${path}/metadata`,
          "too_many_properties",
          `${MAX_METADATA_PROPERTIES}개 이하의 속성만 사용할 수 있습니다.`,
        );
      }
      const validEntries: Array<[string, string]> = [];
      for (const [key, entryValue] of entries) {
        if (
          key.length === 0 ||
          runeLength(key) > MAX_METADATA_KEY_LENGTH ||
          !METADATA_KEY_PATTERN.test(key)
        ) {
          issue(
            issues,
            `${path}/metadata/${key}`,
            "invalid_property_name",
            "metadata key는 128자 이내의 영문자, 숫자, 점, 밑줄, 콜론, 하이픈만 사용할 수 있습니다.",
          );
        } else if (typeof entryValue !== "string") {
          issue(issues, `${path}/metadata/${key}`, "invalid_type", "문자열이어야 합니다.");
        } else if (runeLength(entryValue) > MAX_METADATA_VALUE_LENGTH) {
          issue(
            issues,
            `${path}/metadata/${key}`,
            "too_long",
            `${MAX_METADATA_VALUE_LENGTH}자 이하여야 합니다.`,
          );
        } else {
          validEntries.push([key, entryValue]);
        }
      }
      metadata = Object.fromEntries(validEntries);
    }
  }

  let observedAt: string | undefined;
  if (value.observedAt !== undefined) {
    if (
      typeof value.observedAt !== "string" ||
      !DATE_TIME_PATTERN.test(value.observedAt) ||
      Number.isNaN(Date.parse(value.observedAt))
    ) {
      issue(issues, `${path}/observedAt`, "invalid_date_time", "RFC 3339 date-time이어야 합니다.");
    } else {
      observedAt = value.observedAt;
    }
  }

  if (
    !eventIdValid ||
    !actionValid ||
    !date ||
    !metricNameValid ||
    !metricValueValid
  ) {
    return undefined;
  }
  const metricValue = (metric as Record<string, unknown>).value as number;
  const metricName = (metric as Record<string, unknown>).name as string;
  return {
    eventId: value.eventId as string,
    date: value.date as string,
    action: value.action as string,
    metric: { name: metricName, value: metricValue },
    ...(metadata === undefined ? {} : { metadata }),
    ...(observedAt === undefined ? {} : { observedAt }),
  };
}

export function createIngestPayload(
  events: readonly CustomActivityEventDto[],
  now = new Date(),
): { payload: CustomActivityIngestDto; body: string } {
  const issues: CustomProviderValidationIssue[] = [];
  if (!Array.isArray(events) || events.length < 1 || events.length > MAX_CUSTOM_PROVIDER_BATCH) {
    issue(
      issues,
      "/events",
      "invalid_batch_size",
      `이벤트는 1개 이상 ${MAX_CUSTOM_PROVIDER_BATCH}개 이하여야 합니다.`,
    );
  }
  if (!(now instanceof Date) || Number.isNaN(now.getTime())) {
    issue(issues, "/now", "invalid_date_time", "검증 기준 시각이 올바르지 않습니다.");
  }
  const normalized = Array.isArray(events) && !Number.isNaN(now.getTime())
    ? events.map((event, index) => validateEvent(event, index, now, issues))
    : [];
  if (issues.length > 0) throw new CustomProviderValidationError(issues);

  const payload: CustomActivityIngestDto = {
    schemaVersion: CUSTOM_PROVIDER_SCHEMA_VERSION,
    events: normalized as CustomActivityEventDto[],
  };
  const body = JSON.stringify(payload);
  if (new TextEncoder().encode(body).byteLength > MAX_CUSTOM_PROVIDER_PAYLOAD_BYTES) {
    throw new CustomProviderValidationError([{
      path: "/",
      code: "payload_too_large",
      message: `JSON payload는 ${MAX_CUSTOM_PROVIDER_PAYLOAD_BYTES} bytes 이하여야 합니다.`,
    }]);
  }
  return { payload, body };
}
