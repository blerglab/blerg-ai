-- 009_agent_dataplane.sql: agent-owned project knowledge, separate from the
-- repo's human documents. Embeddings are plain real[] scored in Go — at
-- personal-tool scale (hundreds of memories) an exact scan beats operating a
-- pgvector image swap; the column upgrades to vector(n) later if needed.
CREATE TABLE IF NOT EXISTS agent_memories (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    project     text        NOT NULL,
    name        text        NOT NULL,
    kind        text        NOT NULL DEFAULT 'project',  -- user|feedback|project|reference
    content     text        NOT NULL,
    embedding   real[],
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project, name)
);
CREATE INDEX IF NOT EXISTS agent_memories_project_idx ON agent_memories(project);

CREATE TABLE IF NOT EXISTS agent_rules (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    project     text        NOT NULL,
    content     text        NOT NULL,
    enabled     boolean     NOT NULL DEFAULT false,  -- rules change agent behavior; a human enables
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS agent_rules_project_idx ON agent_rules(project);
