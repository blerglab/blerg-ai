-- Agent "standing rules" (agent_rules, created in 009) are removed in 0.5.0: only Blerg's own
-- built-in agent tools could propose them and no installation used them. Forward-only; the
-- unrelated delegation_rules (standing rules for child-session starts) stay.
DROP TABLE IF EXISTS agent_rules;
