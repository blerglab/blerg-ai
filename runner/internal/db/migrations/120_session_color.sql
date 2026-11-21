-- Empty is None. Stable theme keys, never arbitrary CSS supplied by a client.
ALTER TABLE sessions ADD COLUMN color text NOT NULL DEFAULT ''
 CHECK (color IN ('', 'blue', 'orange', 'purple', 'teal'));
