-- Durable, timing-independent Magic Link delivery. The API stores only an
-- intent. The worker creates the one-time token in memory and activates only
-- its one-way hash and expiry in this FK-free outbox row.
CREATE TABLE IF NOT EXISTS magic_link_mail_outbox (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  token_hash BYTES NULL,
  token_expires_at TIMESTAMPTZ NULL,
  consumed_at TIMESTAMPTZ NULL,
  recipient_email STRING NOT NULL,
  redirect_uri STRING NOT NULL DEFAULT '',
  purpose STRING NOT NULL DEFAULT 'signin',
  status STRING NOT NULL DEFAULT 'pending',
  attempts INT8 NOT NULL DEFAULT 0,
  available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  lease_until TIMESTAMPTZ NULL,
  claim_token UUID NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  terminal_at TIMESTAMPTZ NULL,
  terminal_reason STRING NULL,
  CONSTRAINT magic_link_mail_outbox_recipient_chk CHECK (length(recipient_email) BETWEEN 3 AND 320),
  CONSTRAINT magic_link_mail_outbox_redirect_chk CHECK (length(redirect_uri) <= 2048),
  CONSTRAINT magic_link_mail_outbox_purpose_chk CHECK (purpose = 'signin'),
  CONSTRAINT magic_link_mail_outbox_token_hash_chk CHECK (token_hash IS NULL OR length(token_hash) = 32),
  CONSTRAINT magic_link_mail_outbox_status_chk CHECK (status IN ('pending', 'processing', 'sent', 'dead', 'superseded')),
  CONSTRAINT magic_link_mail_outbox_attempts_chk CHECK (attempts BETWEEN 0 AND 5),
  CONSTRAINT magic_link_mail_outbox_lease_chk CHECK (
    (status = 'processing' AND lease_until IS NOT NULL AND claim_token IS NOT NULL)
    OR (status <> 'processing' AND lease_until IS NULL AND claim_token IS NULL)
  ),
  CONSTRAINT magic_link_mail_outbox_token_state_chk CHECK (
    (status = 'pending' AND token_hash IS NULL AND token_expires_at IS NULL AND consumed_at IS NULL)
    OR (status = 'processing' AND (
      (token_hash IS NULL AND token_expires_at IS NULL AND consumed_at IS NULL)
      OR (token_hash IS NOT NULL AND token_expires_at IS NOT NULL AND token_expires_at > updated_at)
    ))
    OR (status = 'sent' AND token_hash IS NOT NULL AND token_expires_at IS NOT NULL)
    OR (status IN ('dead', 'superseded') AND token_hash IS NULL AND token_expires_at IS NULL AND consumed_at IS NULL)
  ),
  CONSTRAINT magic_link_mail_outbox_terminal_chk CHECK (
    (status IN ('dead', 'superseded') AND terminal_at IS NOT NULL AND terminal_reason IS NOT NULL)
    OR (status NOT IN ('dead', 'superseded') AND terminal_at IS NULL AND terminal_reason IS NULL)
  )
);

CREATE INDEX IF NOT EXISTS magic_link_mail_outbox_available_idx
  ON magic_link_mail_outbox (status, available_at, created_at, id)
  WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS magic_link_mail_outbox_processing_lease_idx
  ON magic_link_mail_outbox (lease_until, id)
  WHERE status = 'processing';

CREATE INDEX IF NOT EXISTS magic_link_mail_outbox_terminal_idx
  ON magic_link_mail_outbox (terminal_at DESC, id)
  WHERE status IN ('dead', 'superseded');
