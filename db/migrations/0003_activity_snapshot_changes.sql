-- Durable, audience-scoped provenance for static ActivitySnapshot responses.
-- Keep markers after an environment is removed so an empty result still
-- reports the deletion. Subject deletion cascades because it is no longer
-- addressable by a snapshot query.
CREATE TABLE activity_snapshot_changes (
  subject_id STRING NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
  environment_id STRING NOT NULL,
  activity_date DATE NOT NULL,
  visibility_scope STRING NOT NULL,
  changed_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (subject_id, environment_id, activity_date, visibility_scope),
  CONSTRAINT activity_snapshot_changes_scope_chk
    CHECK (visibility_scope IN ('public', 'private'))
);
