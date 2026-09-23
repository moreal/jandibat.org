// Compiler fixtures for the Solid 2 pagination/refetch compatibility boundary.
export const subjectQuery = graphql`
  query RelayPaginationCompatQuery($id: String!, $count: Int!, $cursor: Cursor) {
    subject(handleOrID: $id) {
      ...RelayPaginationSubjectFragment @arguments(count: $count, cursor: $cursor)
    }
  }
`;

export const subjectFragment = graphql`
  fragment RelayPaginationSubjectFragment on Subject
  @argumentDefinitions(count: { type: "Int!" }, cursor: { type: "Cursor" })
  @refetchable(queryName: "RelayPaginationSubjectRefetchQuery") {
    id
    providerConnections(first: $count, after: $cursor)
    @connection(key: "RelayPaginationSubject_providerConnections") {
      edges {
        node {
          id
          status
        }
      }
    }
  }
`;
