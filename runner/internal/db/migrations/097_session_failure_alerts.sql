ALTER TABLE sessions ADD COLUMN paused_detail text;
-- Durable failure notices survive both the coordinator and runner restarting.
CREATE TABLE session_failure_alert_epoch (since timestamptz NOT NULL DEFAULT now());
INSERT INTO session_failure_alert_epoch DEFAULT VALUES;
CREATE TABLE session_failure_alerts (
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 occurrence text NOT NULL,
 turn_seq bigint NOT NULL,
 message_id uuid NOT NULL UNIQUE REFERENCES messages(id),
 parent_session_id uuid REFERENCES sessions(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 person_notified boolean NOT NULL DEFAULT false,
 parent_notified boolean NOT NULL DEFAULT false,
 escalated boolean NOT NULL DEFAULT false,
 PRIMARY KEY(session_id, occurrence)
);
