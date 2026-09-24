// Relay compiler source only; runtime pages import generated artifacts.
declare const graphql: (strings: TemplateStringsArray) => unknown;

export const authViewerQuery = graphql`
  query AuthViewerQuery {
    viewer {
      user { id primaryEmail status emailVerifiedAt }
      currentSession { id createdAt expiresAt revokedAt }
    }
  }
`;

export const authSubjectsQuery = graphql`
  query AuthSubjectsQuery($count: Int!, $cursor: Cursor) {
    viewer {
      subjects(first: $count, after: $cursor) {
        edges { node { id handle displayName timezone isPublic createdAt updatedAt } }
        pageInfo { hasNextPage endCursor }
      }
    }
  }
`;

export const authSessionsQuery = graphql`
  query AuthSessionsQuery($count: Int!, $cursor: Cursor) {
    viewer {
      sessions(first: $count, after: $cursor) {
        edges { node { id createdAt expiresAt revokedAt lastSeenAt } }
        pageInfo { hasNextPage endCursor }
      }
    }
  }
`;

export const requestMagicLinkMutation = graphql`
  mutation AuthRequestMagicLinkMutation($input: RequestMagicLinkInput!) {
    requestMagicLink(input: $input) { errors { code message field } accepted }
  }
`;

export const beginPasskeySignInMutation = graphql`
  mutation AuthBeginPasskeySignInMutation {
    beginPasskeySignIn { errors { code message field } options { ceremonyID publicKeyJSON expiresAt } }
  }
`;

export const finishPasskeySignInMutation = graphql`
  mutation AuthFinishPasskeySignInMutation($input: FinishPasskeySignInInput!) {
    finishPasskeySignIn(input: $input) { errors { code message field } session { id expiresAt } }
  }
`;

export const beginPasskeyRegistrationMutation = graphql`
  mutation AuthBeginPasskeyRegistrationMutation {
    beginPasskeyRegistration { errors { code message field } options { ceremonyID publicKeyJSON expiresAt } }
  }
`;

export const finishPasskeyRegistrationMutation = graphql`
  mutation AuthFinishPasskeyRegistrationMutation($input: FinishPasskeyRegistrationInput!) {
    finishPasskeyRegistration(input: $input) { errors { code message field } credential { id label createdAt } }
  }
`;

export const signOutMutation = graphql`
  mutation AuthSignOutMutation { signOut { errors { code message field } session { id revokedAt } } }
`;

export const revokeSessionMutation = graphql`
  mutation AuthRevokeSessionMutation($input: RevokeSessionInput!) {
    revokeSession(input: $input) { errors { code message field } session { id revokedAt } }
  }
`;

export const createSubjectMutation = graphql`
  mutation AuthCreateSubjectMutation($input: CreateSubjectInput!) {
    createSubject(input: $input) {
      errors { code message field }
      subject { id handle displayName timezone isPublic createdAt updatedAt }
    }
  }
`;

export const requestSubjectDeletionMutation = graphql`
  mutation AuthRequestSubjectDeletionMutation($input: RequestSubjectDeletionInput!) {
    requestSubjectDeletion(input: $input) { errors { code message field } request { requestID status } }
  }
`;
