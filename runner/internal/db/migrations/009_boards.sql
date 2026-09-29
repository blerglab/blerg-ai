-- 008_boards.sql: ticket board schema — boards, columns, tickets, and supporting
-- tables. A single ordered migration applied by the existing advisory-locked runner.

-- boards: top-level containers spanning one or more repos.
CREATE TABLE IF NOT EXISTS boards (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name                text        NOT NULL,
    description         text,
    default_daemon_id   uuid        REFERENCES daemons(id),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- board_repos: repos associated with a board (short name, e.g. "example-app").
CREATE TABLE IF NOT EXISTS board_repos (
    board_id    uuid    NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    repo        text    NOT NULL,
    PRIMARY KEY (board_id, repo)
);

-- columns: the lanes of a board; fully dynamic (no hard-coded types).
-- rank is a fractional sort key (LexoRank-style) for ordering within the board.
CREATE TABLE IF NOT EXISTS columns (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    board_id    uuid        NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    rank        text        NOT NULL,
    name        text        NOT NULL,
    is_terminal bool        NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS columns_board_id_rank_idx ON columns(board_id, rank);

-- tickets: cards on a board.
-- column_id is nullable: an archived ticket has column_id = NULL and archived_at set.
-- ON DELETE RESTRICT on column_id keeps live tickets from being orphaned by column
-- deletion; archiving sets column_id = NULL first, making column deletion possible.
-- session_id is SET NULL on session deletion so historical tickets survive session end.
CREATE TABLE IF NOT EXISTS tickets (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    board_id    uuid        NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    column_id   uuid        REFERENCES columns(id) ON DELETE RESTRICT,
    title       text        NOT NULL,
    body        text,
    priority    text        NOT NULL DEFAULT 'medium',
    size        text,
    rank        text        NOT NULL,
    archived_at timestamptz,
    session_id  uuid        REFERENCES sessions(id) ON DELETE SET NULL,
    version     int         NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS tickets_board_id_column_id_rank_idx ON tickets(board_id, column_id, rank);

-- ticket_repos: repos a ticket is scoped to (∈ board_repos); rank = first is primary.
CREATE TABLE IF NOT EXISTS ticket_repos (
    ticket_id   uuid    NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    repo        text    NOT NULL,
    rank        text    NOT NULL,
    PRIMARY KEY (ticket_id, repo)
);

-- ticket_tags: board-scoped tag labels on tickets.
CREATE TABLE IF NOT EXISTS ticket_tags (
    ticket_id   uuid    NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    tag         text    NOT NULL,
    PRIMARY KEY (ticket_id, tag)
);
CREATE INDEX IF NOT EXISTS ticket_tags_tag_idx ON ticket_tags(tag);

-- ticket_dependencies: directed "ticket_id depends on depends_on_ticket_id" edges.
-- Both endpoints cascade so deleting either ticket removes the edge.
CREATE TABLE IF NOT EXISTS ticket_dependencies (
    ticket_id            uuid    NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    depends_on_ticket_id uuid    NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    PRIMARY KEY (ticket_id, depends_on_ticket_id)
);

-- ticket_events: append-only activity feed per ticket.
-- from_column_id / to_column_id are plain nullable UUIDs (no FK) so historical
-- events remain valid after a column is deleted.
CREATE TABLE IF NOT EXISTS ticket_events (
    id              bigserial   PRIMARY KEY,
    ticket_id       uuid        NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    type            text        NOT NULL,
    actor           text        NOT NULL,
    from_column_id  uuid,
    to_column_id    uuid,
    data            jsonb,
    created_at      timestamptz NOT NULL DEFAULT now()
);

-- board_tokens: board-scoped session credentials (SHA-256 hashed; raw token never stored).
-- Cascade on both board and session so tokens are cleaned up on either side.
CREATE TABLE IF NOT EXISTS board_tokens (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    board_id        uuid        NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    session_id      uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    token_hash      bytea       NOT NULL,
    capabilities    text[]      NOT NULL,
    expires_at      timestamptz NOT NULL,
    revoked_at      timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now()
);

-- Extend sessions with board/ticket binding and Assist flag.
-- SET NULL on deletion so session records survive board or ticket removal.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS board_id  uuid REFERENCES boards(id)  ON DELETE SET NULL;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS ticket_id uuid REFERENCES tickets(id) ON DELETE SET NULL;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS assist    bool NOT NULL DEFAULT false;
