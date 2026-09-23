-- @name CountProbeColumns
-- @returns :one
SELECT count(*) FROM information_schema.columns
WHERE table_name = 'probe_items' AND column_name = 'id';
