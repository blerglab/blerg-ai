-- A runner refusing a spawn for lack of slots is a GLOBAL condition — the cap
-- is the runner's, not the card's — so the refusal belongs here, once, and not
-- on whichever card happened to ask. Dispatch reads it before it goes near the
-- ready column: without it, every 20s tick asks a runner that is still full
-- and the card it asked for pays the price in `moved` events.
--
-- Singleton row: one blerg-board talks to one runner, so the key is a constant.
CREATE TABLE runner_capacity (
    id         boolean PRIMARY KEY DEFAULT true CHECK (id),
    refused_at timestamptz NOT NULL DEFAULT now(),
    detail     text NOT NULL DEFAULT ''  -- the runner's own words, for the log
);
