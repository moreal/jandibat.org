import type {
  AuthResultDto,
  CustomActivityIngestDto,
  CustomActivityIngestResponseDto,
  ProblemDto,
} from "@jandibat/contracts";
import type { components, paths } from "../generated/api";
import { defaultApiBaseUrl } from "./runtime-base";

export { defaultApiBaseUrl } from "./runtime-base";
export type OpenApiPaths = paths;

type Schema<Name extends keyof components["schemas"]> = components["schemas"][Name];
type Assert<T extends true> = T;
type Equal<Left, Right> =
  (<Value>() => Value extends Left ? 1 : 2) extends
  (<Value>() => Value extends Right ? 1 : 2)
    ? true
    : false;

/** Only the remaining HTTP edge DTOs are checked against generated OpenAPI. */
export type OpenApiContractAssertions = [
  Assert<Equal<Schema<"MagicLinkConsumeResponse">, AuthResultDto>>,
  Assert<Equal<Schema<"CustomActivityIngestRequest">, CustomActivityIngestDto>>,
  Assert<Equal<Schema<"CustomActivityIngestResponse">, CustomActivityIngestResponseDto>>,
  Assert<Equal<Schema<"Problem">, ProblemDto>>,
];

export class ApiError extends Error {
  readonly status: number;
  readonly problem?: ProblemDto;
  readonly details?: unknown;
  readonly retryAfter?: string;

  constructor(message: string, status: number, details?: unknown, retryAfter?: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.problem = isProblem(details) ? details : undefined;
    this.details = details;
    this.retryAfter = retryAfter;
  }
}

type RequestOptions = Omit<RequestInit, "body"> & { body?: unknown };

/** Browser REST calls are limited to non-GraphQL HTTP edge operations. */
export interface JandibatApi {
  consumeMagicLink(token: string): Promise<AuthResultDto>;
  ingestCustomActivities(
    providerId: string,
    ingestionKey: string,
    input: CustomActivityIngestDto,
    idempotencyKey?: string,
  ): Promise<CustomActivityIngestResponseDto>;
}

export class FetchJandibatApi implements JandibatApi {
  readonly baseUrl: string;
  private readonly useRuntimeBaseUrl: boolean;

  constructor(baseUrl?: string) {
    this.useRuntimeBaseUrl = baseUrl === undefined;
    this.baseUrl = (baseUrl ?? "").replace(/\/$/, "");
  }

  consumeMagicLink(token: string): Promise<AuthResultDto> {
    return this.request("/v1/auth/magic-link/consume", {
      method: "POST",
      body: { token },
    });
  }

  ingestCustomActivities(
    providerId: string,
    ingestionKey: string,
    input: CustomActivityIngestDto,
    idempotencyKey: string = crypto.randomUUID(),
  ): Promise<CustomActivityIngestResponseDto> {
    return this.request(`/v1/custom-providers/${encodeURIComponent(providerId)}/activities:ingest`, {
      method: "POST",
      headers: {
        "X-Jandibat-Provider-Key": ingestionKey,
        "Idempotency-Key": idempotencyKey,
      },
      body: input,
    });
  }

  private async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    const headers = new Headers(options.headers);
    headers.set("Accept", "application/json, application/problem+json");

    let body: BodyInit | undefined;
    if (options.body !== undefined) {
      if (!headers.has("Content-Type")) headers.set("Content-Type", "application/json");
      body = JSON.stringify(options.body);
    }

    let response: Response;
    try {
      response = await fetch(`${this.useRuntimeBaseUrl ? defaultApiBaseUrl() : this.baseUrl}${path}`, {
        ...options,
        credentials: "include",
        headers,
        body,
      });
    } catch (error) {
      if (error instanceof DOMException && error.name === "AbortError") throw error;
      throw new ApiError("API 서버에 연결할 수 없습니다.", 0, error);
    }

    if (response.status === 204) return undefined as T;

    const contentType = response.headers.get("content-type") ?? "";
    const payload: unknown = contentType.includes("json")
      ? await response.json()
      : await response.text();

    if (!response.ok) {
      const problem = isProblem(payload) ? payload : undefined;
      const message = problem?.detail ?? problem?.title ?? `요청을 완료하지 못했습니다. (${response.status})`;
      throw new ApiError(message, response.status, payload, response.headers.get("Retry-After") ?? undefined);
    }

    return payload as T;
  }
}

function isProblem(value: unknown): value is ProblemDto {
  if (typeof value !== "object" || value === null) return false;
  const candidate = value as Partial<ProblemDto>;
  return (
    typeof candidate.type === "string" &&
    typeof candidate.title === "string" &&
    typeof candidate.status === "number" &&
    typeof candidate.code === "string" &&
    typeof candidate.requestId === "string"
  );
}

export const api = new FetchJandibatApi();
