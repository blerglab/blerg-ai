ALTER TABLE sessions ADD COLUMN verbosity text NOT NULL DEFAULT 'standard' CHECK (verbosity IN ('narrated', 'standard', 'terse'));
