-- @name GetSubjectExists
-- @returns :one
SELECT EXISTS (SELECT 1 FROM subjects WHERE id = $1::STRING) AS exists;

-- @name UpsertEnvironment
-- @returns :exec_result
INSERT INTO environments (id, key, name, scope, owner_subject_id, metadata, updated_at)
VALUES ($1::STRING, $2::STRING, $3::STRING, $4::STRING, $5::STRING, $6::JSONB, now())
ON CONFLICT (id) DO UPDATE SET
  key = excluded.key, name = excluded.name, scope = excluded.scope,
  owner_subject_id = excluded.owner_subject_id, metadata = excluded.metadata,
  updated_at = excluded.updated_at
WHERE environments.scope = excluded.scope
  AND environments.owner_subject_id IS NOT DISTINCT FROM excluded.owner_subject_id;

-- @name ListEnvironmentsAll
-- @returns :many
SELECT id, key, name, scope, owner_subject_id, metadata
FROM environments ORDER BY id;

-- @name ListEnvironmentsByIds
-- @returns :many
SELECT id, key, name, scope, owner_subject_id, metadata
FROM environments WHERE id = ANY($1::STRING[]) ORDER BY id;

-- @name EnsurePublicSubject
-- @returns :exec
INSERT INTO subjects (id, owner_user_id, handle, display_name, timezone, is_public)
VALUES ($1::STRING, NULL, $1::STRING, NULL, 'UTC', true)
ON CONFLICT DO NOTHING;

-- @name InsertFactIfMissing
-- @returns :exec
INSERT INTO activity_facts (
  subject_id, environment_id, activity_date, action, metric_name, metric_value,
  metadata, provider_connection_id, custom_provider_id, observed_at, ingested_at
) VALUES ($1::STRING, $2::STRING, $3::DATE, $4::STRING, $5::STRING, $6::INT8,
          $7::JSONB, NULLIF($8::STRING, '')::UUID, NULLIF($9::STRING, '')::UUID, now(), now())
ON CONFLICT DO NOTHING;

-- @name UpdateMatchingFact
-- @returns :exec
UPDATE activity_facts SET metric_value = $6::INT8,
  provider_connection_id = NULLIF($8::STRING, '')::UUID,
  custom_provider_id = NULLIF($9::STRING, '')::UUID,
  observed_at = now(), ingested_at = now()
WHERE subject_id = $1::STRING AND environment_id = $2::STRING
  AND activity_date = $3::DATE AND action = $4::STRING
  AND metric_name = $5::STRING AND metadata = $7::JSONB;

-- @name InsertConnectionFactIfMissing
-- @returns :exec
INSERT INTO activity_facts (
  subject_id, environment_id, activity_date, action, metric_name, metric_value,
  metadata, provider_connection_id, observed_at, ingested_at
) VALUES ($1::STRING, $2::STRING, $3::DATE, $4::STRING, $5::STRING, $6::INT8,
          $7::JSONB, $8::UUID, now(), now())
ON CONFLICT DO NOTHING;

-- @name UpdateConnectionFact
-- @returns :exec
UPDATE activity_facts SET metric_value = $6::INT8,
  provider_connection_id = $8::UUID, observed_at = now(), ingested_at = now()
WHERE subject_id = $1::STRING AND environment_id = $2::STRING
  AND activity_date = $3::DATE AND action = $4::STRING
  AND metric_name = $5::STRING AND metadata = $7::JSONB;

-- @name ListFactsAll
-- @returns :many
SELECT subject_id, activity_date, environment_id, action, metric_name, metric_value, metadata
FROM activity_facts WHERE subject_id = $1::STRING
ORDER BY activity_date, environment_id, action, metric_name, id;

-- @name ListFactsFrom
-- @returns :many
SELECT subject_id, activity_date, environment_id, action, metric_name, metric_value, metadata
FROM activity_facts WHERE subject_id = $1::STRING AND activity_date >= $2::DATE
ORDER BY activity_date, environment_id, action, metric_name, id;

-- @name ListFactsTo
-- @returns :many
SELECT subject_id, activity_date, environment_id, action, metric_name, metric_value, metadata
FROM activity_facts WHERE subject_id = $1::STRING AND activity_date <= $2::DATE
ORDER BY activity_date, environment_id, action, metric_name, id;

-- @name ListFactsBetween
-- @returns :many
SELECT subject_id, activity_date, environment_id, action, metric_name, metric_value, metadata
FROM activity_facts WHERE subject_id = $1::STRING AND activity_date >= $2::DATE AND activity_date <= $3::DATE
ORDER BY activity_date, environment_id, action, metric_name, id;

-- @name DeleteFactsInRange
-- @returns :exec
DELETE FROM activity_facts
WHERE subject_id = $1::STRING AND activity_date >= $2::DATE AND activity_date <= $3::DATE
  AND ($4::BOOL OR environment_id = ANY($5::STRING[]));

-- @name GetCachedAt
-- @returns :opt
SELECT fetched_at FROM activity_refresh_cache
WHERE subject_id = $1::STRING AND environment_id = $2::STRING AND activity_date = $3::DATE;

-- @name UpsertCachedAt
-- @returns :exec
INSERT INTO activity_refresh_cache (subject_id, environment_id, activity_date, fetched_at, updated_at)
VALUES ($1::STRING, $2::STRING, $3::DATE, $4::TIMESTAMPTZ, now())
ON CONFLICT (subject_id, environment_id, activity_date) DO UPDATE SET
  fetched_at = excluded.fetched_at, updated_at = excluded.updated_at;

-- @name DeleteCachedAt
-- @returns :exec
DELETE FROM activity_refresh_cache
WHERE subject_id = $1::STRING AND environment_id = $2::STRING
  AND activity_date = ANY($3::DATE[]);

-- @name LockSyncConnection
-- @returns :opt
SELECT subject_id, environment_id
FROM provider_connections
WHERE id = $1::UUID
  AND COALESCE(sync_cursor->>'connection_status', status) IN ('active', 'error')
  AND sync_cursor->>'sync_execution_claim_token' = $2::STRING
FOR UPDATE;
