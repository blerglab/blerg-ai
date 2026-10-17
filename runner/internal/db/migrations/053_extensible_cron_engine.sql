-- Application capability descriptors enforce which registered engines may run
-- with restricted tools. Keep storage independent of the adapter roster.
ALTER TABLE crons DROP CONSTRAINT crons_engine_check;
ALTER TABLE crons ADD CONSTRAINT crons_engine_check
  CHECK (engine ~ '^[a-z][a-z0-9_-]{0,63}$');
