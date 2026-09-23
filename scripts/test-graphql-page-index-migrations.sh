#!/bin/sh
set -eu

# Use a new isolated database; never remove an existing database or PVC.
database="jandibat_graphql_pages_$(date +%s)_$$"
COCKROACH_DATABASE="$database" sh scripts/db-migrate.sh >/dev/null

sql() {
  docker compose exec -T cockroach cockroach sql --insecure \
    --host=127.0.0.1:26258 --database="$database" --set=errexit=true \
    --format=tsv --execute="$1"
}

versions=$(sql 'SELECT count(*) FROM schema_migrations' | tail -n 1 | tr -d '\r')
if [ "$versions" != 5 ]; then
  echo "GraphQL page migration history has $versions entries, expected 5" >&2
  exit 1
fi

for spec in \
  'provider_connections provider_connections_subject_created_id_idx' \
  'custom_providers custom_providers_subject_created_id_idx'; do
  set -- $spec
  table=$1
  index=$2
  count=$(sql "SELECT count(*) FROM [SHOW INDEXES FROM $table] WHERE index_name = '$index'" | tail -n 1 | tr -d '\r')
  if [ "$count" != 3 ]; then
    echo "GraphQL keyset index $index has $count columns, expected 3" >&2
    exit 1
  fi
  lock=$(sql "SELECT count(*) FROM [SHOW CREATE TABLE $table] WHERE create_statement LIKE '%schema_locked = true%'" | tail -n 1 | tr -d '\r')
  if [ "$lock" != 1 ]; then
    echo "GraphQL keyset migration left $table schema unlocked" >&2
    exit 1
  fi
done

echo "GraphQL page indexes and schema relock passed in isolated database $database"
