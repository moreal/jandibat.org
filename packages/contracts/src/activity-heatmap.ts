/**
 * Application-facing aliases for the OpenAPI schemas used by the web app.
 *
 * Keep these structural types aligned with `openapi/jandibat.yaml`. The web
 * client also performs compile-time compatibility checks against the generated
 * OpenAPI types so a contract change cannot silently drift from these exports.
 */
export type DateDto = string;
export type DateTimeDto = string;
export type ProviderIdDto = "github" | "gitlab" | "codeberg";
export type AuthMethodDto = "oauth2" | "token" | "none";
export type FetchFailurePolicyDto = "keep_stale" | "purge";
export type HeatmapThemeDto =
  | "system"
  | "light"
  | "dark"
  | "github-light"
  | "github-dark";
export type WeekStartDto = "sunday" | "monday";

export interface PageInfoDto {
  nextCursor?: string | null;
  hasNextPage: boolean;
}

export interface ActivityMetricDto {
  name: string;
  value: number;
}

export interface ActivityEntryDto {
  environmentId: string;
  action: string;
  metric: ActivityMetricDto;
  metadata: Record<string, string>;
}

export interface ActivityDayDto {
  date: DateDto;
  count: number;
  /** The OpenAPI contract constrains this number to the inclusive range 0..4. */
  level: number;
  entries: ActivityEntryDto[];
}

export interface EnvironmentDto {
  id: string;
  key: string;
  name: string;
  scope: "global" | "subject";
  ownerSubject?: string | null;
  metadata: Record<string, string>;
}

export interface ActivityTimelineResponseDto {
  subject: string;
  timezone: string;
  from: DateDto;
  to: DateDto;
  generatedAt: DateTimeDto;
  stale: boolean;
  environments: EnvironmentDto[];
  days: ActivityDayDto[];
}

/** Backward-compatible name retained for consumers of the initial scaffold. */
export type ActivityHeatmapResponseDto = ActivityTimelineResponseDto;

export interface ProviderCatalogItemDto {
  id: ProviderIdDto;
  name: string;
  category: "git-hosting";
  authMethods: AuthMethodDto[];
  supportsPrivateData: boolean;
  supportsScheduledSync: boolean;
}

export interface ProviderCatalogResponseDto {
  providers: ProviderCatalogItemDto[];
}

export type ProviderConnectionStatusDto =
  | "active"
  | "pending"
  | "disabled"
  | "revoked"
  | "error";

export type SyncStatusDto =
  | "queued"
  | "running"
  | "succeeded"
  | "failed"
  | "cancelled";

export interface ProviderConnectionDto {
  id: string;
  subjectId: string;
  providerId: ProviderIdDto;
  environmentId: string;
  authMethod: AuthMethodDto;
  status: ProviderConnectionStatusDto;
  externalAccountId?: string | null;
  externalAccountName?: string | null;
  scopes: string[];
  privateDataEnabled: boolean;
  tokenExpiresAt?: DateTimeDto | null;
  lastSyncedAt?: DateTimeDto | null;
  lastSyncStatus?: SyncStatusDto | null;
  lastError?: string | null;
  createdAt: DateTimeDto;
  updatedAt: DateTimeDto;
}

export interface ProviderConnectionsResponseDto {
  connections: ProviderConnectionDto[];
  pageInfo: PageInfoDto;
}

export interface CreateProviderConnectionDto {
  providerId: ProviderIdDto;
  authMethod: AuthMethodDto;
  token?: string;
  scopes?: string[];
  includePrivate: boolean;
  redirectUri?: string;
}

export interface ConnectionStartResponseDto {
  connection: ProviderConnectionDto;
  authorizationUrl?: string;
}

export interface SyncRequestDto {
  from?: DateDto;
  to?: DateDto;
  force: boolean;
  failurePolicy?: FetchFailurePolicyDto;
}

export interface SyncJobDto {
  id: string;
  subjectId: string;
  connectionId: string;
  providerId: ProviderIdDto;
  status: SyncStatusDto;
  requestedAt: DateTimeDto;
  startedAt?: DateTimeDto | null;
  completedAt?: DateTimeDto | null;
  acceptedFacts: number;
  rejectedFacts: number;
  error?: ProblemDto | null;
}

export interface AcceptedResponseDto {
  accepted: true;
}

export interface MagicLinkRequestDto {
  email: string;
  redirectUri?: string;
}

export interface PasskeyOptionsRequestDto {
  email?: string;
  label?: string;
}

export interface PasskeyCeremonyDto {
  ceremonyId: string;
  expiresAt: DateTimeDto;
  publicKey: Record<string, unknown>;
}

export interface WebAuthnCredentialDto {
  id: string;
  rawId: string;
  type: "public-key";
  authenticatorAttachment?: "platform" | "cross-platform" | null;
  response: Record<string, unknown>;
  clientExtensionResults: Record<string, unknown>;
}

export interface PasskeyDto {
  id: string;
  label: string;
  transports: Array<"usb" | "nfc" | "ble" | "internal" | "hybrid">;
  createdAt: DateTimeDto;
  lastUsedAt?: DateTimeDto | null;
}

export interface UserDto {
  id: string;
  primaryEmail: string;
  emailVerifiedAt?: DateTimeDto | null;
  status: "active" | "disabled" | "pending";
  createdAt: DateTimeDto;
  updatedAt: DateTimeDto;
}

export interface SessionDto {
  id: string;
  userId: string;
  current: boolean;
  createdAt: DateTimeDto;
  expiresAt: DateTimeDto;
  lastSeenAt?: DateTimeDto | null;
  revokedAt?: DateTimeDto | null;
  userAgent?: string | null;
  ipAddress?: string | null;
}

export interface AuthResultDto {
  user: UserDto;
  session: SessionDto;
}

export interface SubjectDto {
  id: string;
  handle: string;
  displayName?: string | null;
  timezone: string;
  isPublic: boolean;
  createdAt: DateTimeDto;
  updatedAt: DateTimeDto;
}

export interface CreateSubjectDto {
  handle: string;
  displayName?: string;
  timezone: string;
  isPublic: boolean;
}

export interface SubjectListResponseDto {
  subjects: SubjectDto[];
  pageInfo: PageInfoDto;
}

export interface SubjectDeletionRequestDto {
  requestId: string;
  status: "requested";
}

export interface CustomProviderDto {
  id: string;
  subjectId: string;
  environmentId: string;
  name: string;
  key: string;
  description?: string | null;
  status: "active" | "disabled";
  allowedActions: string[];
  lastIngestedAt?: DateTimeDto | null;
  createdAt: DateTimeDto;
  updatedAt: DateTimeDto;
}

export interface CustomProvidersResponseDto {
  providers: CustomProviderDto[];
  pageInfo: PageInfoDto;
}

export interface CreateCustomProviderDto {
  name: string;
  key: string;
  description?: string;
  allowedActions?: string[];
}

export interface UpdateCustomProviderDto {
  name?: string;
  description?: string | null;
  status?: "active" | "disabled";
  allowedActions?: string[];
}

export interface CustomActivityEventDto {
  eventId: string;
  date: DateDto;
  action: string;
  metric: ActivityMetricDto;
  metadata?: Record<string, string>;
  observedAt?: DateTimeDto;
}

export interface CustomActivityIngestDto {
  schemaVersion: "1.0";
  events: CustomActivityEventDto[];
}

export interface IngestRejectionDto {
  eventId: string;
  code: string;
  detail: string;
}

export interface CustomActivityIngestResponseDto {
  accepted: number;
  duplicates: number;
  rejected: number;
  rejections: IngestRejectionDto[];
}

export interface CustomProviderCreatedDto {
  provider: CustomProviderDto;
  readonly ingestionKey: string;
}

export interface CustomProviderKeyDto {
  readonly ingestionKey: string;
  createdAt: DateTimeDto;
}

export interface FieldErrorDto {
  path: string;
  code: string;
  message: string;
}

export interface ProblemDto {
  type: string;
  title: string;
  status: number;
  detail?: string;
  instance?: string;
  code: string;
  requestId: string;
  errors?: FieldErrorDto[];
}
