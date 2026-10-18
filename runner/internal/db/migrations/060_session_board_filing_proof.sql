-- Server-only launch proof for account-scoped Board discovery. Never part of session JSON.
CREATE TABLE session_board_filing_proof (
 session_id uuid PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
 proof_kind text NOT NULL CHECK (proof_kind IN ('session_id','token_id')),
 proof_value text NOT NULL,
 project_scope text NOT NULL DEFAULT ''
);
