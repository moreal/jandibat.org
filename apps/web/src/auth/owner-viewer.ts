import type { SubjectDto } from "@jandibat/contracts";
import type { Environment, GraphQLTaggedNode, OperationType, VariablesOf } from "relay-runtime";
import type { AuthSubjectsQuery } from "../pages/__generated__/AuthSubjectsQuery.graphql";
import type { AuthSessionsQuery } from "../pages/__generated__/AuthSessionsQuery.graphql";
import type { AuthViewerQuery } from "../pages/__generated__/AuthViewerQuery.graphql";
import subjectsQuery from "../pages/__generated__/AuthSubjectsQuery.graphql";
import sessionsQuery from "../pages/__generated__/AuthSessionsQuery.graphql";
import viewerQuery from "../pages/__generated__/AuthViewerQuery.graphql";
import { readRelayQuery } from "./relay-query";
import { GraphQLNetworkError } from "../relay/environment";

const PAGE_SIZE = 100;

export class SignedOutError extends Error {
  constructor() {
    super("로그인이 필요합니다.");
  }
}

async function readAuthenticatedQuery<TQuery extends OperationType>(
  environment: Environment,
  operation: GraphQLTaggedNode,
  variables: VariablesOf<TQuery>,
  signal?: AbortSignal,
): Promise<TQuery["response"]> {
  try {
    return await readRelayQuery<TQuery>(environment, operation, variables, signal);
  } catch (error) {
    if (error instanceof GraphQLNetworkError && error.code === "UNAUTHENTICATED") {
      throw new SignedOutError();
    }
    throw error;
  }
}

export async function readCurrentViewer(environment: Environment, signal?: AbortSignal) {
  const response = await readAuthenticatedQuery<AuthViewerQuery>(environment, viewerQuery, {}, signal);
  const viewer = response.viewer;
  const expiresAt = viewer ? Date.parse(viewer.currentSession.expiresAt) : NaN;
  if (!viewer || viewer.currentSession.revokedAt || !Number.isFinite(expiresAt) ||
    expiresAt <= Date.now()) {
    throw new SignedOutError();
  }
  return viewer;
}

/** Never infer an empty owner collection from only the first page. */
export async function readOwnedSubjects(environment: Environment, signal?: AbortSignal): Promise<SubjectDto[]> {
  const subjects: SubjectDto[] = [];
  const seen = new Set<string>();
  let cursor: string | null = null;
  do {
    const response: AuthSubjectsQuery["response"] = await readAuthenticatedQuery<AuthSubjectsQuery>(environment, subjectsQuery,
      { count: PAGE_SIZE, cursor }, signal);
    const page: NonNullable<AuthSubjectsQuery["response"]["viewer"]>["subjects"] | undefined = response.viewer?.subjects;
    if (!page) throw new SignedOutError();
    for (const { node } of page.edges) subjects.push({
      id: node.id,
      handle: node.handle,
      displayName: node.displayName ?? undefined,
      timezone: node.timezone,
      isPublic: node.isPublic,
      createdAt: node.createdAt,
      updatedAt: node.updatedAt,
    });
    if (!page.pageInfo.hasNextPage) return subjects;
    const next: string | null = page.pageInfo.endCursor;
    if (!next || seen.has(next)) throw new Error("잔디밭 목록 페이지를 이어갈 수 없습니다.");
    seen.add(next);
    cursor = next;
  } while (true);
}

export type OwnedSession = {
  id: string;
  createdAt: string;
  expiresAt: string;
  revokedAt: string | null;
  lastSeenAt: string | null;
};

export async function readOwnedSessions(environment: Environment, signal?: AbortSignal): Promise<OwnedSession[]> {
  const sessions: OwnedSession[] = [];
  const seen = new Set<string>();
  let cursor: string | null = null;
  do {
    const response: AuthSessionsQuery["response"] = await readAuthenticatedQuery<AuthSessionsQuery>(environment,
      sessionsQuery, { count: PAGE_SIZE, cursor }, signal);
    const page: NonNullable<AuthSessionsQuery["response"]["viewer"]>["sessions"] | undefined = response.viewer?.sessions;
    if (!page) throw new SignedOutError();
    for (const { node } of page.edges) sessions.push({
      id: node.id,
      createdAt: node.createdAt,
      expiresAt: node.expiresAt,
      revokedAt: node.revokedAt ?? null,
      lastSeenAt: node.lastSeenAt ?? null,
    });
    if (!page.pageInfo.hasNextPage) return sessions;
    const next: string | null = page.pageInfo.endCursor;
    if (!next || seen.has(next)) throw new Error("세션 목록 페이지를 이어갈 수 없습니다.");
    seen.add(next);
    cursor = next;
  } while (true);
}
