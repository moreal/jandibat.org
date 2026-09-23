-- @name GetUserByID
-- @returns :one
SELECT id, primary_email, status, email_verified_at, created_at, updated_at
FROM users WHERE id = $1::STRING;
