-- The merge outcome, on the card itself: what landed and when — the board
-- can then say "done, merged 241262b, live" at a glance.
ALTER TABLE cards ADD COLUMN merged_sha text;
ALTER TABLE cards ADD COLUMN merged_at timestamptz;

-- Backfill from the merge comments that already record it.
UPDATE cards c SET
    merged_sha = sub.sha,
    merged_at  = sub.at
FROM (
    SELECT DISTINCT ON (card_id) card_id,
           substring(data->>'text' FROM ' as ([0-9a-f]{7,40})') AS sha,
           created_at AS at
    FROM card_events
    WHERE type = 'comment'
      AND (data->>'text' LIKE 'Auto-merged%' OR data->>'text' LIKE 'Accepted by human review — merged%')
    ORDER BY card_id, created_at DESC
) sub
WHERE sub.card_id = c.id AND sub.sha IS NOT NULL;
