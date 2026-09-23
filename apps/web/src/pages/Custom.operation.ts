// Relay compiler source only; runtime pages import generated artifacts.
declare const graphql: (strings: TemplateStringsArray) => unknown;

export const customQuery = graphql`
  query CustomQuery($subject: String!, $count: Int!, $cursor: Cursor) {
    subject(handleOrID: $subject) {
      id
      ...CustomSubjectFragment @arguments(count: $count, cursor: $cursor)
    }
  }
`;

export const customSubjectFragment = graphql`
  fragment CustomSubjectFragment on Subject
  @argumentDefinitions(count: { type: "Int!" }, cursor: { type: "Cursor" })
  @refetchable(queryName: "CustomSubjectRefetchQuery") {
    id
    customProviders(first: $count, after: $cursor)
    @connection(key: "CustomSubject_customProviders") {
      edges {
        node {
          id ingestProviderID slug name description status allowedActions createdAt updatedAt
        }
      }
      pageInfo { hasNextPage endCursor }
    }
  }
`;

export const customCreateMutation = graphql`
  mutation CustomCreateProviderMutation($input: CreateCustomProviderInput!) {
    createCustomProvider(input: $input) {
      errors { code message field }
      provider { id }
      ingestionKey
    }
  }
`;

export const customUpdateMutation = graphql`
  mutation CustomUpdateProviderMutation($input: UpdateCustomProviderInput!) {
    updateCustomProvider(input: $input) {
      errors { code message field }
      provider { id name description status allowedActions updatedAt }
    }
  }
`;

export const customRotateMutation = graphql`
  mutation CustomRotateProviderKeyMutation($input: RotateCustomProviderKeyInput!) {
    rotateCustomProviderKey(input: $input) {
      errors { code message field }
      ingestionKey
      createdAt
    }
  }
`;

export const customDeleteMutation = graphql`
  mutation CustomDeleteProviderMutation($input: DeleteCustomProviderInput!) {
    deleteCustomProvider(input: $input) {
      errors { code message field }
      deletedProviderID
    }
  }
`;
