-- Per-board dispatch concurrency: how many cards a board run may have in
-- flight at once. The dispatcher has enforced this limit since 007, but
-- nothing could ever set it, so every board ran exactly one card at a time
-- forever. This is the knob.
--
-- It lives on `boards`, not on `board_runs`, because a run row is inserted
-- fresh every time someone presses Run — a value set there would evaporate on
-- the next start. It is also the operator's cross-board priority dial (bump
-- the project being pushed on, leave the rest at 1), which only works if it
-- outlives a single run.
--
-- 0 is legal and meaningful: "park this board". The run stays active and keeps
-- its counters, in-flight cards finish, and nothing new is dispatched.
-- Negatives are not — the API rejects them before they reach here, and this
-- CHECK is the backstop for anything that goes round it.
ALTER TABLE boards ADD COLUMN concurrency int NOT NULL DEFAULT 1
    CHECK (concurrency >= 0);

-- board_runs.concurrency goes, rather than being seeded from the board at
-- Run: two columns holding the same number is how the original one ended up
-- unwritable, and a per-run snapshot actively fights the point of the dial.
-- Turning it is how the operator re-prioritises a board NOW — dropping a
-- board to 0 has to park the run that is already going, not the next one — so
-- the dispatcher reads the board's live value on every tick.
--
-- No data is lost: nothing has ever written this column, so every row holds
-- the default. But a pre-015 binary SELECTs it on every tick, which costs a
-- window in both directions:
--
--   * forward, during a rolling deploy — the new pod migrates while the old
--     one is still serving, so the old pod's dispatch pauses until it exits.
--     Seconds, and self-healing.
--   * backward — rolling the image back without restoring this column stops
--     dispatch on every board, indefinitely, until the binary rolls forward.
--
-- Only dispatch is affected either way: in the pre-015 runTick this SELECT
-- comes after the auto-merge sweep and the reapers, so those keep working.
ALTER TABLE board_runs DROP COLUMN concurrency;
