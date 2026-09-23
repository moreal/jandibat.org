#!/bin/sh
set -eu

schema=graphql/schema.graphql
for declaration in \
  'subjects(first: Int = 25, after: Cursor): SubjectConnection!' \
  'settings: UserSettings!' \
  'type SubjectConnection' \
  'type SubjectSettings' \
  'updateUserSettings(input: UpdateUserSettingsInput!): UpdateUserSettingsPayload!' \
  'createSubject(input: CreateSubjectInput!): CreateSubjectPayload!' \
  'updateSubject(input: UpdateSubjectInput!): UpdateSubjectPayload!' \
  'updateSubjectSettings(input: UpdateSubjectSettingsInput!): UpdateSubjectSettingsPayload!' \
  'requestSubjectDeletion(input: RequestSubjectDeletionInput!): RequestSubjectDeletionPayload!'; do
  if ! grep -Fq "$declaration" "$schema"; then
    echo "GraphQL Subject contract is missing: $declaration" >&2
    exit 1
  fi
done
if grep -Eq '^type (UserSettings|SubjectSettings|SubjectEdge|SubjectConnection|DeletionRequestResult) implements Node' "$schema"; then
  echo 'settings, page and deletion request must remain value objects' >&2
  exit 1
fi
echo 'GraphQL Subject queries and typed mutation payloads are present'
