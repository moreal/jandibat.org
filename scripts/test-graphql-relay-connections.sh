#!/bin/sh
set -eu

schema=graphql/schema.graphql
type_body() {
  awk -v name="$1" '$0 ~ ("^type " name "( implements Node)? [{]$") { active = 1; next }
    active && /^}/ { exit }
    active { print }' "$schema"
}
require_field() {
  owner=$1
  declaration=$2
  if ! type_body "$owner" | grep -Fq "$declaration"; then
    echo "Relay connection contract is missing $owner.$declaration" >&2
    exit 1
  fi
}

require_field Query 'viewer: Viewer'
require_field Viewer 'currentSession: Session!'
require_field Viewer 'sessions(first: Int = 25, after: Cursor): SessionConnection!'
require_field ProviderConnection 'syncJobs(first: Int = 25, after: Cursor): SyncJobConnection'
for owner in Viewer ProviderConnection; do
  require_field "$owner" 'first: 1..100'
done
require_field SessionConnection 'edges: [SessionEdge!]!'
require_field SyncJobConnection 'edges: [SyncJobEdge!]!'
require_field SessionConnection 'pageInfo: PageInfo!'
require_field SyncJobConnection 'pageInfo: PageInfo!'
require_field SessionEdge 'cursor: Cursor!'
require_field SessionEdge 'node: Session!'
require_field SyncJobEdge 'cursor: Cursor!'
require_field SyncJobEdge 'node: SyncJob!'
for declaration in \
  'hasNextPage: Boolean!' \
  'hasPreviousPage: Boolean!' \
  'startCursor: Cursor' \
  'endCursor: Cursor'; do
  require_field PageInfo "$declaration"
done

if grep -Eq '^type (SessionEdge|SyncJobEdge|PageInfo) implements Node' "$schema"; then
  echo 'Relay connection value objects must not implement Node' >&2
  exit 1
fi

echo 'Relay Session and SyncJob connection contract is present'
