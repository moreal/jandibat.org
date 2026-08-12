import type {
  CustomActivityEventDto,
  CustomActivityIngestResponseDto,
} from "@jandibat/contracts";
import {
  CustomProviderApiError,
  CustomProviderNetworkError,
  CustomProviderResponseError,
  CustomProviderValidationError,
} from "./errors.ts";
import { createIngestPayload } from "./validation.ts";

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const PROBLEM_CODE_PATTERN = /^[a-z0-9_]{1,128}$/;
const MAX_API_BASE_LENGTH = 2_048;
const MAX_INGESTION_KEY_LENGTH = 4_096;
const MIN_INGESTION_KEY_LENGTH = 32;
const MIN_IDEMPOTENCY_KEY_LENGTH = 8;
const MAX_IDEMPOTENCY_KEY_LENGTH = 255;
const ALLOWED_CONFIG_KEYS = new Set(["apiBaseUrl", "providerId", "ingestionKey", "fetch", "now"]);
const ALLOWED_PUSH_OPTION_KEYS = new Set(["idempotencyKey", "signal"]);

export type CustomProviderClientConfig = {
  apiBaseUrl: string;
  providerId: string;
  ingestionKey: string;
  fetch?: typeof globalThis.fetch;
  now?: () => Date;
};

export type CustomProviderPushOptions = {
  idempotencyKey: string;
  signal?: AbortSignal;
};

function validationError(path: string, code: string, message: string): never {
  throw new CustomProviderValidationError([{ path, code, message }]);
}

function validateApiBase(value: unknown): string {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    value.length > MAX_API_BASE_LENGTH ||
    value !== value.trim() ||
    /[\u0000-\u001f\u007f]/.test(value)
  ) {
    validationError("/apiBaseUrl", "invalid_api_base", "API base는 유효한 HTTPS URL이어야 합니다.");
  }
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    validationError("/apiBaseUrl", "invalid_api_base", "API base는 유효한 HTTPS URL이어야 합니다.");
  }
  if (
    url.protocol !== "https:" ||
    !url.hostname ||
    url.username ||
    url.password ||
    url.search ||
    url.hash
  ) {
    validationError("/apiBaseUrl", "invalid_api_base", "API base는 credential, query, fragment가 없는 HTTPS URL이어야 합니다.");
  }
  try {
    if (/[\u0000-\u001f\u007f]/.test(decodeURIComponent(url.pathname))) {
      validationError("/apiBaseUrl", "invalid_api_base", "API base path가 올바르지 않습니다.");
    }
  } catch {
    validationError("/apiBaseUrl", "invalid_api_base", "API base path가 올바르지 않습니다.");
  }
  return url.href.replace(/\/$/, "");
}

function validateProviderId(value: unknown): string {
  if (typeof value !== "string" || !UUID_PATTERN.test(value)) {
    validationError("/providerId", "invalid_provider_id", "providerId는 UUID여야 합니다.");
  }
  return value;
}

function validateIngestionKey(value: unknown): string {
  if (
    typeof value !== "string" ||
    Array.from(value).length < MIN_INGESTION_KEY_LENGTH ||
    Array.from(value).length > MAX_INGESTION_KEY_LENGTH ||
    /[\r\n]/.test(value)
  ) {
    validationError(
      "/ingestionKey",
      "invalid_ingestion_key",
      `ingestionKey는 ${MIN_INGESTION_KEY_LENGTH}~${MAX_INGESTION_KEY_LENGTH}자의 header-safe 문자열이어야 합니다.`,
    );
  }
  return value;
}

export function createIdempotencyKey(): string {
  if (typeof globalThis.crypto?.randomUUID !== "function") {
    throw new CustomProviderValidationError([{
      path: "/idempotencyKey",
      code: "secure_random_unavailable",
      message: "안전한 멱등키를 생성할 수 없습니다. idempotencyKey를 직접 전달해 주세요.",
    }]);
  }
  return globalThis.crypto.randomUUID();
}

function validateIdempotencyKey(value: unknown): string {
  if (
    typeof value !== "string" ||
    value !== value.trim() ||
    Array.from(value).length < MIN_IDEMPOTENCY_KEY_LENGTH ||
    Array.from(value).length > MAX_IDEMPOTENCY_KEY_LENGTH ||
    /[\r\n]/.test(value)
  ) {
    validationError(
      "/idempotencyKey",
      "invalid_idempotency_key",
      `idempotencyKey는 ${MIN_IDEMPOTENCY_KEY_LENGTH}~${MAX_IDEMPOTENCY_KEY_LENGTH}자의 header-safe 문자열이어야 합니다.`,
    );
  }
  return value;
}

function objectValue(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function parseResponse(value: unknown, eventCount: number): CustomActivityIngestResponseDto {
  if (!objectValue(value) || !Array.isArray(value.rejections)) {
    throw new CustomProviderResponseError();
  }
  const counts = [value.accepted, value.duplicates, value.rejected];
  if (counts.some((count) => typeof count !== "number" || !Number.isInteger(count) || count < 0)) {
    throw new CustomProviderResponseError();
  }
  const rejections = value.rejections.map((item) => {
    if (
      !objectValue(item) ||
      typeof item.eventId !== "string" ||
      typeof item.code !== "string" ||
      typeof item.detail !== "string"
    ) {
      throw new CustomProviderResponseError();
    }
    return { eventId: item.eventId, code: item.code, detail: item.detail };
  });
  const accepted = value.accepted as number;
  const duplicates = value.duplicates as number;
  const rejected = value.rejected as number;
  if (
    accepted + duplicates + rejected !== eventCount ||
    rejected !== rejections.length ||
    rejections.length > eventCount
  ) {
    throw new CustomProviderResponseError();
  }
  return { accepted, duplicates, rejected, rejections };
}

async function apiError(response: Response): Promise<CustomProviderApiError> {
  let code = "request_failed";
  let requestId = response.headers.get("X-Request-ID") ?? undefined;
  const contentType = response.headers.get("Content-Type") ?? "";
  if (contentType.includes("json")) {
    try {
      const value: unknown = await response.json();
      if (objectValue(value)) {
        if (typeof value.code === "string" && PROBLEM_CODE_PATTERN.test(value.code)) {
          code = value.code;
        }
        if (typeof value.requestId === "string" && value.requestId.length <= 128) {
          requestId = value.requestId;
        }
      }
    } catch {
      // The stable, secret-free status error below is sufficient.
    }
  }
  return new CustomProviderApiError(
    response.status,
    code,
    requestId,
    response.headers.get("Retry-After") ?? undefined,
  );
}

export class CustomProviderClient {
  readonly #apiBaseUrl: string;
  readonly #providerId: string;
  readonly #ingestionKey: string;
  readonly #fetch: typeof globalThis.fetch;
  readonly #now: () => Date;

  constructor(config: CustomProviderClientConfig) {
    if (!objectValue(config)) {
      validationError("/", "invalid_config", "SDK 설정은 객체여야 합니다.");
    }
    for (const key of Object.keys(config)) {
      if (!ALLOWED_CONFIG_KEYS.has(key)) {
        validationError(`/${key}`, "unknown_config", "지원하지 않는 SDK 설정입니다.");
      }
    }
    this.#apiBaseUrl = validateApiBase(config.apiBaseUrl);
    this.#providerId = validateProviderId(config.providerId);
    this.#ingestionKey = validateIngestionKey(config.ingestionKey);
    const fetchImplementation = config.fetch ?? globalThis.fetch;
    if (typeof fetchImplementation !== "function") {
      validationError("/fetch", "fetch_unavailable", "Fetch 구현이 필요합니다.");
    }
    this.#fetch = fetchImplementation;
    this.#now = config.now ?? (() => new Date());
    if (typeof this.#now !== "function") {
      validationError("/now", "invalid_clock", "now는 함수여야 합니다.");
    }
  }

  async push(
    events: readonly CustomActivityEventDto[],
    options: CustomProviderPushOptions,
  ): Promise<CustomActivityIngestResponseDto> {
    if (!objectValue(options)) {
      validationError("/options", "invalid_options", "push options가 필요합니다.");
    }
    for (const key of Object.keys(options)) {
      if (!ALLOWED_PUSH_OPTION_KEYS.has(key)) {
        validationError(`/options/${key}`, "unknown_option", "지원하지 않는 push option입니다.");
      }
    }
    const { body } = createIngestPayload(events, this.#now());
    const idempotencyKey = validateIdempotencyKey(options.idempotencyKey);
    const endpoint = `${this.#apiBaseUrl}/v1/custom-providers/${encodeURIComponent(this.#providerId)}/activities:ingest`;
    let response: Response;
    try {
      response = await this.#fetch(endpoint, {
        method: "POST",
        credentials: "omit",
        redirect: "error",
        referrerPolicy: "no-referrer",
        signal: options.signal,
        headers: {
          Accept: "application/json, application/problem+json",
          "Content-Type": "application/json",
          "Idempotency-Key": idempotencyKey,
          "X-Jandibat-Provider-Key": this.#ingestionKey,
        },
        body,
      });
    } catch (error) {
      if (error instanceof DOMException && error.name === "AbortError") throw error;
      throw new CustomProviderNetworkError();
    }
    if (!response.ok || response.status !== 202) throw await apiError(response);
    if (!(response.headers.get("Content-Type") ?? "").includes("json")) {
      throw new CustomProviderResponseError();
    }
    let value: unknown;
    try {
      value = await response.json();
    } catch {
      throw new CustomProviderResponseError();
    }
    return parseResponse(value, events.length);
  }

  toJSON(): Record<string, never> {
    return {};
  }
}

export function createCustomProviderClient(
  config: CustomProviderClientConfig,
): CustomProviderClient {
  return new CustomProviderClient(config);
}
