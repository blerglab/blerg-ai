-- Components now self-describe: a registering component sends its whole agent
-- manifest entry (description, docs/openapi/mcp URLs, auth block, operations
-- table), not just name/base_url/version/capabilities. Rather than one column
-- per field — every one of which would need another migration as the contract
-- grows additively — the entry is stored verbatim as JSON here and merged back
-- on read, with the dedicated columns (which the landing page and the stale
-- check still query directly) remaining authoritative for the fields they hold.
-- Existing rows default to '{}': they simply have no extra fields until their
-- component re-registers, which every component does on boot.
ALTER TABLE components ADD COLUMN manifest jsonb NOT NULL DEFAULT '{}';
