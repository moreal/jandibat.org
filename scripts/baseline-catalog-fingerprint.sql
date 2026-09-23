-- Fingerprint the public schema without database-specific IDs or timestamps.
-- Keep column/default, index, constraint, check, and FK definitions visible.
WITH catalog AS (
  SELECT 'tables' AS kind, table_name AS detail
  FROM information_schema.tables
  WHERE table_schema = 'public' AND table_type = 'BASE TABLE'
    AND table_name <> 'schema_migrations'
  UNION ALL
  SELECT 'columns', concat_ws('|', table_name, lpad(ordinal_position::STRING, 3, '0'),
    column_name, data_type, is_nullable, coalesce(column_default, ''))
  FROM information_schema.columns
  WHERE table_schema = 'public' AND table_name <> 'schema_migrations'
  UNION ALL
  SELECT 'indexes', concat_ws('|', table_name, index_name,
    lpad(seq_in_index::STRING, 3, '0'), coalesce(column_name, ''),
    coalesce(direction, ''), coalesce(storing, ''), coalesce(implicit, ''),
    coalesce(non_unique, ''), coalesce(is_visible, ''))
  FROM information_schema.statistics
  WHERE table_schema = 'public' AND table_name <> 'schema_migrations'
  UNION ALL
  SELECT 'constraints', concat_ws('|', table_name, constraint_name, constraint_type)
  FROM information_schema.table_constraints
  WHERE table_schema = 'public' AND table_name <> 'schema_migrations'
    AND constraint_name !~ '^[0-9]+_[0-9]+_[0-9]+_not_null$'
  UNION ALL
  SELECT 'checks', concat_ws('|', tc.table_name, cc.constraint_name, cc.check_clause)
  FROM information_schema.check_constraints AS cc
  JOIN information_schema.table_constraints AS tc
    ON (tc.constraint_catalog, tc.constraint_schema, tc.constraint_name) =
       (cc.constraint_catalog, cc.constraint_schema, cc.constraint_name)
  WHERE tc.table_schema = 'public' AND tc.table_name <> 'schema_migrations'
    AND cc.constraint_name !~ '^[0-9]+_[0-9]+_[0-9]+_not_null$'
  UNION ALL
  SELECT 'foreign_keys', concat_ws('|', tc.table_name, rc.constraint_name,
    rc.unique_constraint_name, rc.update_rule, rc.delete_rule)
  FROM information_schema.referential_constraints AS rc
  JOIN information_schema.table_constraints AS tc
    ON (tc.constraint_catalog, tc.constraint_schema, tc.constraint_name) =
       (rc.constraint_catalog, rc.constraint_schema, rc.constraint_name)
  WHERE tc.table_schema = 'public' AND tc.table_name <> 'schema_migrations'
  UNION ALL
  SELECT 'schema_locks', regexp_extract(create_statement,
    'CREATE TABLE public[.]([A-Za-z0-9_]+)')
  FROM [SHOW CREATE ALL TABLES]
  WHERE create_statement LIKE '%schema_locked = true%'
    AND create_statement NOT LIKE 'CREATE TABLE public.schema_migrations %'
)
SELECT kind, count(*) AS entries, sha256(string_agg(detail, e'\n' ORDER BY detail)) AS digest
FROM catalog
GROUP BY kind
ORDER BY kind;
