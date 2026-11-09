-- Only CLI polls opt in; native waiters retain their existing delivery path.
ALTER TABLE messages ADD COLUMN cli_poll_until timestamptz;
