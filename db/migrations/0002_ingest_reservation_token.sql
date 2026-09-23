-- Fence each ingest idempotency reservation independently of its key and payload.
-- Expired reservations may be replaced; a delayed former owner must not finish
-- or release the replacement even when the request hash is identical.
-- jandibat:schema-unlock ingest_idempotency_keys
ALTER TABLE ingest_idempotency_keys
  ADD COLUMN reservation_token UUID NOT NULL DEFAULT gen_random_uuid();
