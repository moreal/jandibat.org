-- Subject-scoped Relay keyset traversal without sorting every custom provider.
-- jandibat:schema-unlock custom_providers
CREATE INDEX custom_providers_subject_created_id_idx
  ON custom_providers (subject_id, created_at, id);
