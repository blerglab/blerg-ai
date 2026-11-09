-- Retain terminal authorization outcomes under the caller's original launch key.
ALTER TABLE runner_idempotency ADD COLUMN refusal_code text NOT NULL DEFAULT '';
ALTER TABLE runner_idempotency ADD COLUMN refusal_via text NOT NULL DEFAULT '';
