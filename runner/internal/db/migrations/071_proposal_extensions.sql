-- A human may extend a still-pending proposal without rewriting its creation time or request.
ALTER TABLE mcp_proposals ADD COLUMN IF NOT EXISTS expires_at_override timestamptz;
CREATE TABLE IF NOT EXISTS proposal_extensions (
  id bigserial PRIMARY KEY,
  proposal_id uuid NOT NULL REFERENCES mcp_proposals(id) ON DELETE CASCADE,
  extended_by text NOT NULL,
  extended_at timestamptz NOT NULL DEFAULT now(),
  previous_expires_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  CHECK (expires_at > previous_expires_at)
);
CREATE INDEX IF NOT EXISTS proposal_extensions_proposal_idx ON proposal_extensions(proposal_id, id);
