ALTER TABLE sessions ADD COLUMN worker_kind text NOT NULL DEFAULT '' CHECK (worker_kind IN ('', 'card', 'role', 'review', 'research'));
