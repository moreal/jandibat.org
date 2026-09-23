-- @name GetProbeItem
-- @returns :one
SELECT id FROM probe_items WHERE id = $1::STRING;
