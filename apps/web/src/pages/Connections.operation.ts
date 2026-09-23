// Relay compiler source only; runtime pages import generated artifacts.
declare const graphql: (strings: TemplateStringsArray) => unknown;

export const connectionsQuery = graphql`
  query ConnectionsQuery($subject: String!, $count: Int!, $cursor: Cursor) {
    providerCatalog {
      id name description kind category supportsOAuth supportsToken supportsPrivateData
    }
    subject(handleOrID: $subject) {
      id
      ...ConnectionsSubjectFragment @arguments(count: $count, cursor: $cursor)
    }
  }
`;

export const connectionsSubjectFragment = graphql`
  fragment ConnectionsSubjectFragment on Subject
  @argumentDefinitions(count: { type: "Int!" }, cursor: { type: "Cursor" })
  @refetchable(queryName: "ConnectionsSubjectRefetchQuery") {
    id
    providerConnections(first: $count, after: $cursor)
    @connection(key: "ConnectionsSubject_providerConnections") {
      edges {
        node {
          id providerID authMethod status privateDataEnabled lastSyncedAt
        }
      }
      pageInfo { hasNextPage endCursor }
    }
  }
`;

export const connectProviderMutation = graphql`
  mutation ConnectionsConnectProviderMutation($input: ConnectProviderInput!) {
    connectProvider(input: $input) {
      errors { code message field }
      authorizationURL
      connection { id providerID authMethod status privateDataEnabled lastSyncedAt }
    }
  }
`;

export const revokeProviderMutation = graphql`
  mutation ConnectionsRevokeProviderMutation($input: RevokeProviderConnectionInput!) {
    revokeProviderConnection(input: $input) {
      errors { code message field }
      revokedConnectionID
    }
  }
`;

export const enqueueSyncMutation = graphql`
  mutation ConnectionsEnqueueSyncMutation($input: EnqueueManualSyncInput!) {
    enqueueManualSync(input: $input) {
      errors { code message field }
      job { id status }
    }
  }
`;
