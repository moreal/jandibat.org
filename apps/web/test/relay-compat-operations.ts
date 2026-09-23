// Compiler-only fixtures for the Solid 2 / Relay compatibility boundary.
// The test imports generated artifacts rather than evaluating these tags.
export const query = graphql`
  query RelayCompatQuery($id: String!) {
    subject(handleOrID: $id) {
      ...RelayCompatSubjectFragment
    }
  }
`;

export const subjectFragment = graphql`
  fragment RelayCompatSubjectFragment on Subject {
    id
    displayName
  }
`;

export const updateSubject = graphql`
  mutation RelayCompatUpdateSubjectMutation($input: UpdateSubjectInput!) {
    updateSubject(input: $input) {
      errors {
        code
        field
        message
      }
      subject {
        id
        displayName
      }
    }
  }
`;
