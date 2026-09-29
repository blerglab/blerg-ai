-- 001_init.sql — blerg-board full schema.

CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE boards (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name                text NOT NULL,
    description         text,
    default_runner      text,
    field_schema        jsonb NOT NULL DEFAULT '[]',
    require_repo        boolean NOT NULL DEFAULT true,
    gate_enabled        boolean NOT NULL DEFAULT false,
    gate_on_unavailable text NOT NULL DEFAULT 'open'
                        CHECK (gate_on_unavailable IN ('open','hold')),
    gate_on_dispute     text NOT NULL DEFAULT 'open'
                        CHECK (gate_on_dispute IN ('open','tiebreak','hold')),
    -- Per-board human-facing card number sequence, allocated under the board
    -- row lock (the same lock that serialises rank allocation).
    next_card_number    integer NOT NULL DEFAULT 1,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE board_repos (
    board_id uuid NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    repo     text NOT NULL,
    PRIMARY KEY (board_id, repo)
);

CREATE TABLE board_columns (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    board_id    uuid NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    rank        text NOT NULL,
    name        text NOT NULL,
    is_terminal boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX board_columns_board_rank_idx ON board_columns (board_id, rank);

CREATE TABLE tokens (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    board_id     uuid REFERENCES boards(id) ON DELETE CASCADE, -- NULL for service keys
    kind         text NOT NULL CHECK (kind IN ('service','agent')),
    label        text NOT NULL,
    token_hash   bytea NOT NULL UNIQUE,          -- SHA-256; raw token never stored
    capabilities text[] NOT NULL DEFAULT '{}',
    -- Stable across refreshes: a refreshed token is a new row sharing its
    -- predecessor's lineage. Durable references point at the lineage.
    lineage_id   uuid NOT NULL,
    expires_at   timestamptz,
    revoked_at   timestamptz,
    last_used_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tokens_lineage_idx ON tokens (lineage_id);

CREATE TABLE cards (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    board_id    uuid NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    number      integer NOT NULL,               -- human-facing "#42", per board
    column_id   uuid REFERENCES board_columns(id) ON DELETE RESTRICT, -- NULL = archived
    type        text NOT NULL DEFAULT 'task',   -- lowercased on write
    title       text NOT NULL,
    body        text,
    priority    text NOT NULL DEFAULT 'medium'
                CHECK (priority IN ('low','medium','high','urgent')),
    size        text CHECK (size IN ('XS','S','M','L','XL')),
    fields      jsonb NOT NULL DEFAULT '{}',    -- validated against boards.field_schema
    dedup_key   text,                            -- agent-supplied intent hash
    external_id text,                            -- e.g. blerg-ops's INC-20260731-01
    rank        text NOT NULL,
    search_vector tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
        setweight(to_tsvector('english', coalesce(body, '')),  'B')
    ) STORED,
    archived_at timestamptz,
    version     integer NOT NULL DEFAULT 0,     -- bumped by EVERY mutation
    gate_flag   text CHECK (gate_flag IN ('ungated','forced','tiebroken','held_approved')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (board_id, number)
);
-- Archived cards excluded so a previously archived finding can be refiled.
CREATE UNIQUE INDEX cards_dedup_key_uidx ON cards (board_id, dedup_key)
    WHERE dedup_key IS NOT NULL AND archived_at IS NULL;
CREATE UNIQUE INDEX cards_external_id_uidx ON cards (board_id, external_id)
    WHERE external_id IS NOT NULL;
CREATE INDEX cards_board_column_rank_idx ON cards (board_id, column_id, rank);
CREATE INDEX cards_search_idx ON cards USING GIN (search_vector);
CREATE INDEX cards_title_trgm_idx ON cards USING GIN (title gin_trgm_ops);

-- Repo is first-class (NOT a card_link): a standing agent resolves its working
-- tree from the card's primary repo. Validated ⊆ board_repos in code.
CREATE TABLE card_repos (
    card_id uuid NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
    repo    text NOT NULL,
    rank    text NOT NULL,                       -- first by rank = primary working dir
    PRIMARY KEY (card_id, repo)
);

CREATE TABLE card_links (
    card_id uuid NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
    kind    text NOT NULL CHECK (kind IN ('session','pr','rcca','doc','url')),
    url     text NOT NULL,
    label   text,
    rank    text NOT NULL,
    PRIMARY KEY (card_id, kind, url)
);

CREATE TABLE card_tags (
    card_id uuid NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
    tag     text NOT NULL,
    PRIMARY KEY (card_id, tag)
);
CREATE INDEX card_tags_tag_idx ON card_tags (tag);

CREATE TABLE card_dependencies (
    card_id            uuid NOT NULL REFERENCES cards(id) ON DELETE CASCADE, -- the blocked card
    depends_on_card_id uuid NOT NULL REFERENCES cards(id) ON DELETE CASCADE, -- the blocker
    PRIMARY KEY (card_id, depends_on_card_id),
    CHECK (card_id <> depends_on_card_id)
);

CREATE TABLE admission_reviews (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    board_id           uuid NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    submitted_at       timestamptz NOT NULL DEFAULT now(),
    actor_token_id     uuid REFERENCES tokens(id),   -- NULL for human
    operation          text NOT NULL
                       CHECK (operation IN ('create','update','delete','move','archive')),
    payload_hash       bytea NOT NULL,
    payload            jsonb NOT NULL,
    verdict            text CHECK (verdict IN ('accept','deny','revise','held')),
    reason             text,
    duplicate_of       uuid,
    model              text,
    backend            text,
    latency_ms         integer,
    confidence         real,
    policy_applied     text NOT NULL
                       CHECK (policy_applied IN
                       ('gated','ungated','forced','tiebroken','held','repeat_rejected','override')),
    dispute_of         uuid REFERENCES admission_reviews(id),
    tiebreak_review_id uuid REFERENCES admission_reviews(id),
    resolved_by        text CHECK (resolved_by IN ('curator','tiebreaker','human','timeout')),
    resolved_at        timestamptz,
    held_expires_at    timestamptz,
    -- NULLABLE and load-bearing: a denied submission never becomes a card.
    card_id            uuid REFERENCES cards(id) ON DELETE SET NULL
);
CREATE INDEX admission_reviews_board_idx ON admission_reviews (board_id, submitted_at DESC);
CREATE INDEX admission_reviews_hash_idx  ON admission_reviews (board_id, payload_hash);
CREATE INDEX admission_reviews_held_idx  ON admission_reviews (board_id)
    WHERE verdict = 'held' AND resolved_at IS NULL;

CREATE TABLE card_events (
    id               bigserial PRIMARY KEY,
    card_id          uuid NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
    type             text NOT NULL,
    actor            text NOT NULL CHECK (actor IN ('human','agent','service','curator')),
    actor_token_id   uuid REFERENCES tokens(id),
    actor_session_id uuid,                       -- human browser session
    review_id        uuid REFERENCES admission_reviews(id),
    -- Deliberately FK-free so events survive column deletion.
    from_column_id   uuid,
    to_column_id     uuid,
    data             jsonb NOT NULL DEFAULT '{}',
    created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX card_events_card_idx ON card_events (card_id, id);

CREATE TABLE standing_agents (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    board_id         uuid NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    -- The agent references the column (a column may have several agents).
    column_id        uuid NOT NULL REFERENCES board_columns(id) ON DELETE CASCADE,
    name             text NOT NULL,
    runner           text NOT NULL,
    prompt           text NOT NULL,
    token_lineage_id uuid NOT NULL,              -- lineage, not a token row: refresh-safe
    session_mode     text NOT NULL DEFAULT 'per_card'
                     CHECK (session_mode IN ('per_card','persistent')),
    enabled          boolean NOT NULL DEFAULT true,
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE standing_agent_queue (
    id                bigserial PRIMARY KEY,
    standing_agent_id uuid NOT NULL REFERENCES standing_agents(id) ON DELETE CASCADE,
    card_id           uuid NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
    enqueued_at       timestamptz NOT NULL DEFAULT now(),
    attempts          integer NOT NULL DEFAULT 0,
    next_attempt_at   timestamptz NOT NULL DEFAULT now(),
    state             text NOT NULL DEFAULT 'pending'
                      CHECK (state IN ('pending','running','done','failed')),
    last_error        text
);
-- Coalescing: a card entering/leaving/re-entering while pending enqueues once.
CREATE UNIQUE INDEX standing_agent_queue_open_uidx
    ON standing_agent_queue (standing_agent_id, card_id)
    WHERE state IN ('pending','running');

CREATE TABLE runner_sessions (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    card_id             uuid REFERENCES cards(id) ON DELETE CASCADE, -- NULL for persistent standing sessions
    board_id            uuid NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    runner              text NOT NULL,
    external_session_id text NOT NULL,
    lifecycle           text NOT NULL DEFAULT 'starting',
    runtime             text NOT NULL DEFAULT '',
    resumable           boolean NOT NULL DEFAULT false,
    last_seq            bigint NOT NULL DEFAULT 0,   -- ingest cursor; runner_events is authoritative
    last_activity_at    timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE runner_events (
    id                bigserial PRIMARY KEY,
    runner_session_id uuid NOT NULL REFERENCES runner_sessions(id) ON DELETE CASCADE,
    seq               bigint NOT NULL,
    ts                timestamptz NOT NULL,
    -- Closed set defined by the runner contract; transient deltas never stored.
    kind              text NOT NULL
                      CHECK (kind IN ('user_turn','assistant_turn','tool_call','tool_result','error','status')),
    payload           jsonb NOT NULL DEFAULT '{}',
    UNIQUE (runner_session_id, seq)
);
