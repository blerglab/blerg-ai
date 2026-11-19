-- BLG-444: a card's worker-time budget crossing fires at most once per account, board, card and
-- absolute threshold (owner's decision 3, corrected: dedup keys on the threshold's own absolute
-- seconds, never an interval-relative ordinal, so a later change to the account's interval can
-- only add newly-reached thresholds — it can never re-announce or skip one already recorded).
-- The row is the durable "already fired" lock, the same idiom session_failure_alerts already
-- proves survives a restart (097_session_failure_alerts.sql).
CREATE TABLE card_time_threshold_alerts (
 account_id       text    NOT NULL,
 board_id         text    NOT NULL,
 card_number      integer NOT NULL CHECK(card_number>0 AND card_number<=10000000),
 threshold_seconds bigint NOT NULL CHECK(threshold_seconds>0),
 fired_at         timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(account_id,board_id,card_number,threshold_seconds)
);

-- BLG-444 (owner's decision 4, corrected): the budget interval is per account, not install-wide —
-- account A must never be able to read or change account B's threshold.
CREATE TABLE card_time_budget_settings (
 account_id     text    PRIMARY KEY,
 interval_hours integer NOT NULL CHECK(interval_hours>=1 AND interval_hours<=168),
 updated_by     text    NOT NULL,
 updated_at     timestamptz NOT NULL DEFAULT now()
);

-- BLG-444 (Codex round 2): a card's worker-time total is read as one bounded query, not
-- maintained in a separate ledger table (the ledger drifted from the live truth via
-- check-then-insert races and a one-directional invalidation trigger — see docs/design/
-- card-time.md). The read finds every session directly linked to (board_id,card_number) via the
-- existing session_card_links_card_idx(board_id,card_number), then checks each one's own full
-- link set to confirm it names exactly this one card. That second step already uses
-- session_card_links' own primary key (session_id leads it), so it needs no index of its own;
-- this index only makes the FIRST step's session_id extraction index-only, avoiding a heap fetch
-- per linked session on a path that runs at every worker's turn-end.
CREATE INDEX session_card_links_card_session_idx ON session_card_links(board_id,card_number,session_id);
