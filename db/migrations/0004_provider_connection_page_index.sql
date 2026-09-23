-- Subject-scoped Relay keyset traversal without sorting every connection.
-- jandibat:schema-unlock provider_connections
CREATE INDEX provider_connections_subject_created_id_idx
  ON provider_connections (subject_id, created_at, id);
