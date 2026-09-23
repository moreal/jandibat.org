-- @name ClaimPublicSubject
-- @returns :opt
UPDATE subjects
SET owner_user_id = $2::STRING,
    display_name = NULL,
    timezone = $3::STRING,
    is_public = $4::BOOL,
    updated_at = $5::TIMESTAMPTZ
WHERE handle = $1::STRING AND owner_user_id IS NULL
RETURNING id, owner_user_id, handle, display_name, timezone, is_public, created_at, updated_at;

-- @name ClaimPublicSubjectWithDisplay
-- @returns :opt
UPDATE subjects
SET owner_user_id = $2::STRING, display_name = $3::STRING, timezone = $4::STRING,
    is_public = $5::BOOL, updated_at = $6::TIMESTAMPTZ
WHERE handle = $1::STRING AND owner_user_id IS NULL
RETURNING id, owner_user_id, handle, display_name, timezone, is_public, created_at, updated_at;

-- @name InsertSubject
-- @returns :exec_result
INSERT INTO subjects (id, owner_user_id, handle, display_name, timezone, is_public, created_at, updated_at)
VALUES ($1::STRING, $2::STRING, $3::STRING, $4::STRING, $5::STRING, $6::BOOL, $7::TIMESTAMPTZ, $8::TIMESTAMPTZ);

-- @name UpsertSubjectSettings
-- @returns :exec_result
INSERT INTO subject_settings
  (subject_id, default_theme, week_start, sync_enabled, sync_interval_minutes, failure_policy, updated_at)
VALUES ($1::STRING, $2::STRING, $3::STRING, $4::BOOL, $5::INT4, $6::STRING, $7::TIMESTAMPTZ)
ON CONFLICT (subject_id) DO UPDATE SET
  default_theme = excluded.default_theme,
  week_start = excluded.week_start,
  sync_enabled = excluded.sync_enabled,
  sync_interval_minutes = excluded.sync_interval_minutes,
  failure_policy = excluded.failure_policy,
  updated_at = excluded.updated_at;

-- @name GetSubjectByIdentifier
-- @returns :opt
SELECT id, owner_user_id, handle, display_name, timezone, is_public, created_at, updated_at
FROM subjects
WHERE id = $1::STRING OR handle = $1::STRING
ORDER BY CASE WHEN id = $1::STRING THEN 0 ELSE 1 END
LIMIT 1;

-- @name ListSubjectsFirstPage
-- @returns :many
SELECT id, owner_user_id, handle, display_name, timezone, is_public, created_at, updated_at
FROM subjects
WHERE owner_user_id = $1::STRING
ORDER BY created_at DESC, id
LIMIT $2::INT8;

-- @name ListSubjectsAfter
-- @returns :many
SELECT id, owner_user_id, handle, display_name, timezone, is_public, created_at, updated_at
FROM subjects
WHERE owner_user_id = $1::STRING
  AND (created_at < $2::TIMESTAMPTZ OR (created_at = $2::TIMESTAMPTZ AND id > $3::STRING))
ORDER BY created_at DESC, id
LIMIT $4::INT8;

-- @name UpdateSubject
-- @returns :exec_result
UPDATE subjects
SET handle = $2::STRING, display_name = $3::STRING, updated_at = $4::TIMESTAMPTZ
WHERE id = $1::STRING AND owner_user_id = $5::STRING
  AND timezone = $6::STRING AND is_public = $7::BOOL;

-- @name UpdateSubjectWithoutDisplay
-- @returns :exec_result
UPDATE subjects SET handle = $2::STRING, display_name = NULL, updated_at = $3::TIMESTAMPTZ
WHERE id = $1::STRING AND owner_user_id = $4::STRING AND timezone = $5::STRING AND is_public = $6::BOOL;

-- @name SubjectExists
-- @returns :one
SELECT EXISTS (SELECT 1 FROM subjects WHERE id = $1::STRING) AS exists;

-- @name LockSubject
-- @returns :one
SELECT id FROM subjects WHERE id = $1::STRING FOR UPDATE;

-- @name LockSubjectConnections
-- @returns :many
SELECT id FROM provider_connections WHERE subject_id = $1::STRING FOR UPDATE;

-- @name QueueSubjectRevocations
-- @returns :exec_result
INSERT INTO provider_token_revocation_jobs (
  connection_id, provider_id, token_ciphertext, token_key_id,
  status, attempts, available_at, created_at, updated_at
)
SELECT id, COALESCE(sync_cursor->>'provider_id', ''), access_token_ciphertext,
       access_token_key_id, 'pending', 0, now(), now(), now()
FROM provider_connections
WHERE subject_id = $1::STRING AND auth_method = 'oauth2'
  AND access_token_ciphertext IS NOT NULL;

-- @name DeleteSubject
-- @returns :exec_result
DELETE FROM subjects WHERE id = $1::STRING;

-- @name GetSubjectSettings
-- @returns :opt
SELECT ss.subject_id, s.timezone, s.is_public, ss.default_theme, ss.week_start,
       ss.sync_enabled, ss.sync_interval_minutes, ss.failure_policy, ss.updated_at
FROM subject_settings AS ss
JOIN subjects AS s ON s.id = ss.subject_id
WHERE ss.subject_id = $1::STRING;

-- @name UpdateSubjectPreferences
-- @returns :exec_result
UPDATE subject_settings
SET default_theme = $2::STRING, week_start = $3::STRING, sync_enabled = $4::BOOL,
    sync_interval_minutes = $5::INT4, failure_policy = $6::STRING, updated_at = $7::TIMESTAMPTZ
WHERE subject_id = $1::STRING;

-- @name UpdateSubjectResource
-- @returns :exec_result
UPDATE subjects SET timezone = $2::STRING, is_public = $3::BOOL, updated_at = $4::TIMESTAMPTZ
WHERE id = $1::STRING;

-- @name UpdateActiveUser
-- @returns :exec_result
UPDATE users
SET primary_email = $2::STRING, email_verified_at = $3::TIMESTAMPTZ,
    updated_at = GREATEST(updated_at, $4::TIMESTAMPTZ)
WHERE id = $1::STRING AND status = 'active';

-- @name UpdateActiveUserWithoutVerification
-- @returns :exec_result
UPDATE users SET primary_email = $2::STRING, email_verified_at = NULL,
    updated_at = GREATEST(updated_at, $3::TIMESTAMPTZ)
WHERE id = $1::STRING AND status = 'active';

-- @name InsertUserSettingsIfMissing
-- @returns :exec_result
INSERT INTO user_settings (user_id, locale, timezone, theme, updated_at)
VALUES ($1::STRING, $2::STRING, $3::STRING, $4::STRING, $5::TIMESTAMPTZ)
ON CONFLICT (user_id) DO NOTHING;

-- @name GetUser
-- @returns :opt
SELECT id, primary_email, email_verified_at, status, created_at, updated_at
FROM users WHERE id = $1::STRING;

-- @name GetUserSettings
-- @returns :opt
SELECT locale, timezone, theme, updated_at FROM user_settings WHERE user_id = $1::STRING;

-- @name UpdateUserSettings
-- @returns :exec_result
UPDATE user_settings
SET locale = $2::STRING, timezone = $3::STRING, theme = $4::STRING, updated_at = $5::TIMESTAMPTZ
WHERE user_id = $1::STRING;
