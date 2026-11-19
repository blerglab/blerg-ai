-- Notes written before parent delivery existed were already shown to the person.
-- Baseline them once so upgrading does not replay the historical backlog to Main.
UPDATE messages SET child_parent_notified=true WHERE kind='note';
