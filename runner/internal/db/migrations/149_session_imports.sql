-- BLG-479: per-child bookkeeping of completed `session import` (cluster-workers-from-desktop.md
-- §4) runs. Lets Main ask "what's the next import number for this child" and "which published
-- result bundles has this child's own import not yet caught up to" without trusting anything a
-- bundle itself claims. One row per completed import, keyed by the exact artifact (id, version)
-- it landed — a re-report of the same one is harmless (ON CONFLICT DO NOTHING at the call site).
CREATE TABLE session_imports (
    session_id       uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    artifact_id      text NOT NULL,
    artifact_version int NOT NULL,
    n                int NOT NULL,
    imported_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, artifact_id, artifact_version)
);
CREATE INDEX session_imports_session_n_idx ON session_imports(session_id, n);
