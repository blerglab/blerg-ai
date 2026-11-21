-- Shared scheduling turns; payload-free and independent of provider cadence.
CREATE SEQUENCE IF NOT EXISTS live_mcp_owner_turn;
CREATE TABLE IF NOT EXISTS live_mcp_owner_schedule (
 owner_id uuid PRIMARY KEY,
 last_claimed_turn bigint NOT NULL CHECK(last_claimed_turn>0)
);
