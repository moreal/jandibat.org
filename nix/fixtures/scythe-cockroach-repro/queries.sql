-- @name UpsertProbeItem
-- @returns :one
UPSERT INTO probe_items (id) VALUES ($1::STRING) RETURNING id;
