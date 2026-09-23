import type {
  AcceptedResponseDto,
  ActivityTimelineResponseDto,
  AuthMethodDto,
  AuthResultDto,
  ConnectionStartResponseDto,
  CreateSubjectDto,
  CreateCustomProviderDto,
  CreateProviderConnectionDto,
  CustomActivityIngestDto,
  CustomActivityIngestResponseDto,
  CustomProviderCreatedDto,
  CustomProviderDto,
  CustomProviderKeyDto,
  CustomProvidersResponseDto,
  MagicLinkRequestDto,
  PasskeyCeremonyDto,
  PasskeyDto,
  ProblemDto,
  ProviderCatalogResponseDto,
  ProviderConnectionsResponseDto,
  SyncJobDto,
  SyncRequestDto,
  SubjectDto,
  SubjectDeletionRequestDto,
  SubjectListResponseDto,
  UpdateCustomProviderDto,
  WebAuthnCredentialDto,
} from "@jandibat/contracts";
import type { components, paths } from "../generated/api";
import { defaultApiBaseUrl } from "./runtime-base";
export { defaultApiBaseUrl } from "./runtime-base";

export type OpenApiPaths = paths;
export type PasskeyOptions = Record<string, unknown>;

type Schema<Name extends keyof components["schemas"]> = components["schemas"][Name];
type Assert<T extends true> = T;
type Equal<Left, Right> =
  (<Value>() => Value extends Left ? 1 : 2) extends
  (<Value>() => Value extends Right ? 1 : 2)
    ? true
    : false;

/**
 * A generated-schema compatibility gate for every contract DTO consumed by
 * this client. `tsc` fails here when the OpenAPI document changes without the
 * application-facing contracts being updated.
 */
export type OpenApiContractAssertions = [
  Assert<Equal<Schema<"ActivityTimelineResponse">, ActivityTimelineResponseDto>>,
  Assert<Equal<Schema<"ProviderCatalogResponse">, ProviderCatalogResponseDto>>,
  Assert<Equal<Schema<"ProviderConnectionListResponse">, ProviderConnectionsResponseDto>>,
  Assert<Equal<Schema<"CreateProviderConnectionRequest">, CreateProviderConnectionDto>>,
  Assert<Equal<Schema<"ProviderConnectionCreateResponse">, ConnectionStartResponseDto>>,
  Assert<Equal<Schema<"SyncRequest">, SyncRequestDto>>,
  Assert<Equal<Schema<"SyncJob">, SyncJobDto>>,
  Assert<Equal<Schema<"AcceptedResponse">, AcceptedResponseDto>>,
  Assert<Equal<Schema<"MagicLinkRequest">, MagicLinkRequestDto>>,
  Assert<Equal<Schema<"PasskeyOptionsResponse">, PasskeyCeremonyDto>>,
  Assert<Equal<Schema<"Passkey">, PasskeyDto>>,
  Assert<Equal<Schema<"WebAuthnCredential">, WebAuthnCredentialDto>>,
  Assert<Equal<Schema<"AuthResult">, AuthResultDto>>,
  Assert<Equal<Schema<"Subject">, SubjectDto>>,
  Assert<Equal<Schema<"CreateSubjectRequest">, CreateSubjectDto>>,
  Assert<Equal<Schema<"SubjectListResponse">, SubjectListResponseDto>>,
  Assert<Equal<Schema<"SubjectDeletionRequestResponse">, SubjectDeletionRequestDto>>,
  Assert<Equal<Schema<"CustomProviderListResponse">, CustomProvidersResponseDto>>,
  Assert<Equal<Schema<"CustomProviderCreatedResponse">, CustomProviderCreatedDto>>,
  Assert<Equal<Schema<"CustomProviderKeyResponse">, CustomProviderKeyDto>>,
  Assert<Equal<Schema<"CreateCustomProviderRequest">, CreateCustomProviderDto>>,
  Assert<Equal<Schema<"UpdateCustomProviderRequest">, UpdateCustomProviderDto>>,
  Assert<Equal<Schema<"CustomActivityIngestRequest">, CustomActivityIngestDto>>,
  Assert<Equal<Schema<"CustomActivityIngestResponse">, CustomActivityIngestResponseDto>>,
  Assert<Equal<Schema<"Problem">, ProblemDto>>,
];

export class ApiError extends Error {
  readonly status: number;
  readonly problem?: ProblemDto;
  readonly details?: unknown;
  readonly retryAfter?: string;

  constructor(
    message: string,
    status: number,
    details?: unknown,
    retryAfter?: string,
  ) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.problem = isProblem(details) ? details : undefined;
    this.details = details;
    this.retryAfter = retryAfter;
  }
}

type RequestOptions = Omit<RequestInit, "body"> & {
  body?: unknown;
};

export type ActivityQuery = {
  from?: string;
  to?: string;
  timezone?: string;
  force?: boolean;
  environmentIds?: string[];
  signal?: AbortSignal;
};

export type ProviderConnectInput = {
  authMethod: AuthMethodDto;
  token?: string;
  scopes?: string[];
  includePrivate: boolean;
  redirectUri?: string;
};

export interface JandibatApi {
  getActivities(subject: string, query?: ActivityQuery): Promise<ActivityTimelineResponseDto>;
  listProviderCatalog(signal?: AbortSignal): Promise<ProviderCatalogResponseDto>;
  listConnections(subject: string, signal?: AbortSignal): Promise<ProviderConnectionsResponseDto>;
  connectProvider(
    subject: string,
    provider: CreateProviderConnectionDto["providerId"],
    input: ProviderConnectInput,
  ): Promise<ConnectionStartResponseDto>;
  disconnectProvider(subject: string, connectionId: string): Promise<void>;
  syncProvider(
    subject: string,
    connectionId: string,
    input?: SyncRequestDto,
  ): Promise<SyncJobDto>;
  getSyncJob(jobId: string, signal?: AbortSignal): Promise<SyncJobDto>;
  requestMagicLink(input: MagicLinkRequestDto): Promise<AcceptedResponseDto>;
  consumeMagicLink(token: string): Promise<AuthResultDto>;
  getCurrentSession(signal?: AbortSignal): Promise<AuthResultDto>;
  signOut(): Promise<void>;
  listSubjects(signal?: AbortSignal): Promise<SubjectListResponseDto>;
  createSubject(input: CreateSubjectDto): Promise<SubjectDto>;
  requestSubjectDeletion(subject: string): Promise<SubjectDeletionRequestDto>;
  beginPasskeyRegistration(label?: string): Promise<PasskeyCeremonyDto>;
  finishPasskeyRegistration(
    ceremonyId: string,
    credential: WebAuthnCredentialDto,
    label?: string,
  ): Promise<PasskeyDto>;
  beginPasskeyAuthentication(email?: string): Promise<PasskeyCeremonyDto>;
  finishPasskeyAuthentication(
    ceremonyId: string,
    credential: WebAuthnCredentialDto,
  ): Promise<AuthResultDto>;
  listCustomProviders(subject: string, signal?: AbortSignal): Promise<CustomProvidersResponseDto>;
  createCustomProvider(subject: string, input: CreateCustomProviderDto): Promise<CustomProviderCreatedDto>;
  updateCustomProvider(
    subject: string,
    providerId: string,
    input: UpdateCustomProviderDto,
  ): Promise<CustomProviderDto>;
  deleteCustomProvider(subject: string, providerId: string): Promise<void>;
  rotateCustomProviderKey(subject: string, providerId: string): Promise<CustomProviderKeyDto>;
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

  async getActivities(
    subject: string,
    query: ActivityQuery = {},
  ): Promise<ActivityTimelineResponseDto> {
    const params = new URLSearchParams();
    if (query.from) params.set("from", query.from);
    if (query.to) params.set("to", query.to);
    if (query.timezone) params.set("timezone", query.timezone);
    if (query.force !== undefined) params.set("force", String(query.force));
    for (const environmentId of query.environmentIds ?? []) {
      params.append("environmentId", environmentId);
    }
    const suffix = params.size > 0 ? `?${params.toString()}` : "";
    return this.request<ActivityTimelineResponseDto>(
      `/v1/activities/${encodeURIComponent(subject)}${suffix}`,
      { signal: query.signal },
    );
  }

  listProviderCatalog(signal?: AbortSignal): Promise<ProviderCatalogResponseDto> {
    return this.request("/v1/providers", { signal });
  }

  listConnections(subject: string, signal?: AbortSignal): Promise<ProviderConnectionsResponseDto> {
    return this.request(`/v1/subjects/${encodeURIComponent(subject)}/provider-connections`, { signal });
  }

  connectProvider(
    subject: string,
    provider: CreateProviderConnectionDto["providerId"],
    input: ProviderConnectInput,
  ): Promise<ConnectionStartResponseDto> {
    const body: CreateProviderConnectionDto = {
      providerId: provider,
      authMethod: input.authMethod,
      token: input.token,
      scopes: input.scopes,
      includePrivate: input.includePrivate,
      redirectUri: input.redirectUri,
    };
    return this.request<ConnectionStartResponseDto>(`/v1/subjects/${encodeURIComponent(subject)}/provider-connections`, {
      method: "POST",
      body,
    }).then((response) => {
      if (input.authMethod === "oauth2" && !response.authorizationUrl) {
        throw new ApiError("OAuth 연결 응답에 이동할 주소가 없습니다.", 502, response);
      }
      return response;
    });
  }

  async disconnectProvider(subject: string, connectionId: string): Promise<void> {
    await this.request(`/v1/subjects/${encodeURIComponent(subject)}/provider-connections/${encodeURIComponent(connectionId)}`, {
      method: "DELETE",
    });
  }

  syncProvider(
    subject: string,
    connectionId: string,
    input: SyncRequestDto = { force: false },
  ): Promise<SyncJobDto> {
    return this.request(`/v1/subjects/${encodeURIComponent(subject)}/provider-connections/${encodeURIComponent(connectionId)}/sync`, {
      method: "POST",
      headers: { "Idempotency-Key": crypto.randomUUID() },
      body: input,
    });
  }

  getSyncJob(jobId: string, signal?: AbortSignal): Promise<SyncJobDto> {
    return this.request(`/v1/sync-jobs/${encodeURIComponent(jobId)}`, { signal });
  }

  requestMagicLink(input: MagicLinkRequestDto): Promise<AcceptedResponseDto> {
    return this.request("/v1/auth/magic-link/request", {
      method: "POST",
      body: input,
    });
  }

  consumeMagicLink(token: string): Promise<AuthResultDto> {
    return this.request("/v1/auth/magic-link/consume", {
      method: "POST",
      body: { token },
    });
  }

  getCurrentSession(signal?: AbortSignal): Promise<AuthResultDto> {
    return this.request("/v1/auth/session", { signal });
  }

  async signOut(): Promise<void> {
    await this.request("/v1/auth/session", { method: "DELETE" });
  }

  listSubjects(signal?: AbortSignal): Promise<SubjectListResponseDto> {
    return this.request("/v1/subjects", { signal });
  }

  createSubject(input: CreateSubjectDto): Promise<SubjectDto> {
    return this.request("/v1/subjects", {
      method: "POST",
      body: input,
    });
  }

  requestSubjectDeletion(subject: string): Promise<SubjectDeletionRequestDto> {
    return this.request(`/v1/subjects/${encodeURIComponent(subject)}`, {
      method: "DELETE",
    });
  }

  beginPasskeyRegistration(label?: string): Promise<PasskeyCeremonyDto> {
    return this.request("/v1/auth/passkey/register/options", {
      method: "POST",
      body: label ? { label } : {},
    });
  }

  finishPasskeyRegistration(
    ceremonyId: string,
    credential: WebAuthnCredentialDto,
    label?: string,
  ): Promise<PasskeyDto> {
    return this.request("/v1/auth/passkey/register/finish", {
      method: "POST",
      body: { ceremonyId, credential, label },
    });
  }

  beginPasskeyAuthentication(email?: string): Promise<PasskeyCeremonyDto> {
    return this.request("/v1/auth/passkey/sign-in/options", {
      method: "POST",
      body: email ? { email } : {},
    });
  }

  finishPasskeyAuthentication(
    ceremonyId: string,
    credential: WebAuthnCredentialDto,
  ): Promise<AuthResultDto> {
    return this.request("/v1/auth/passkey/sign-in/finish", {
      method: "POST",
      body: { ceremonyId, credential },
    });
  }

  listCustomProviders(subject: string, signal?: AbortSignal): Promise<CustomProvidersResponseDto> {
    return this.request(`/v1/subjects/${encodeURIComponent(subject)}/custom-providers`, { signal });
  }

  createCustomProvider(subject: string, input: CreateCustomProviderDto): Promise<CustomProviderCreatedDto> {
    return this.request(`/v1/subjects/${encodeURIComponent(subject)}/custom-providers`, {
      method: "POST",
      body: input,
    });
  }

  updateCustomProvider(
    subject: string,
    providerId: string,
    input: UpdateCustomProviderDto,
  ): Promise<CustomProviderDto> {
    return this.request(`/v1/subjects/${encodeURIComponent(subject)}/custom-providers/${encodeURIComponent(providerId)}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/merge-patch+json" },
      body: input,
    });
  }

  async deleteCustomProvider(subject: string, providerId: string): Promise<void> {
    await this.request(`/v1/subjects/${encodeURIComponent(subject)}/custom-providers/${encodeURIComponent(providerId)}`, {
      method: "DELETE",
    });
  }

  rotateCustomProviderKey(subject: string, providerId: string): Promise<CustomProviderKeyDto> {
    return this.request(`/v1/subjects/${encodeURIComponent(subject)}/custom-providers/${encodeURIComponent(providerId)}/rotate-key`, {
      method: "POST",
    });
  }

  ingestCustomActivities(
    providerId: string,
    ingestionKey: string,
    input: CustomActivityIngestDto,
    idempotencyKey = crypto.randomUUID(),
  ): Promise<CustomActivityIngestResponseDto> {
    return this.request(
      `/v1/custom-providers/${encodeURIComponent(providerId)}/activities:ingest`,
      {
        method: "POST",
        headers: {
          "X-Jandibat-Provider-Key": ingestionKey,
          "Idempotency-Key": idempotencyKey,
        },
        body: input,
      },
    );
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
      throw new ApiError(
        message,
        response.status,
        payload,
        response.headers.get("Retry-After") ?? undefined,
      );
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
