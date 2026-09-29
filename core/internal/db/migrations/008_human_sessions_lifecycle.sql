ALTER TABLE human_sessions
    ADD COLUMN expires_at  timestamptz NOT NULL DEFAULT now() + interval '30 days',
    ADD COLUMN chain_id    uuid,
    ADD COLUMN replaced_by uuid REFERENCES human_sessions(id);
UPDATE human_sessions SET chain_id = id WHERE chain_id IS NULL;
ALTER TABLE human_sessions ALTER COLUMN chain_id SET NOT NULL;
CREATE INDEX human_sessions_live_idx ON human_sessions (account_id, last_used_at DESC NULLS LAST) WHERE revoked_at IS NULL;
CREATE INDEX human_sessions_chain_idx ON human_sessions (chain_id);
ALTER TABLE human_sessions DROP CONSTRAINT human_sessions_account_id_fkey,
    ADD CONSTRAINT human_sessions_account_id_fkey FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE;
ALTER TABLE user_credentials DROP CONSTRAINT user_credentials_account_id_fkey,
    ADD CONSTRAINT user_credentials_account_id_fkey FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE;
