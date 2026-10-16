ALTER TABLE crons DROP CONSTRAINT crons_engine_check;
ALTER TABLE crons ADD CONSTRAINT crons_engine_check CHECK (engine IN ('claude', 'codex'));
