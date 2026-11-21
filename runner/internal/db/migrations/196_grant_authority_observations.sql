-- A durable high-water mark fences results from concurrent Core requests.
-- The journal remains the transition audit and notices use the existing outbox.
CREATE TABLE grant_authority_observations (
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 grant_key text NOT NULL,
 state text NOT NULL DEFAULT '',
 authority text NOT NULL DEFAULT '',
 asked_at timestamptz NOT NULL DEFAULT '1970-01-01 UTC',
 unavailable_since timestamptz,
 unavailable_notified boolean NOT NULL DEFAULT false,
 adopted_by text,
 PRIMARY KEY(session_id, grant_key)
);

-- Preserve pre-upgrade transition history. Old keys can be adopted lazily once
-- the observer supplies original provenance (which the old journal did not store).
WITH observations AS (
 SELECT session_id,payload::jsonb->>'grant_key' AS grant_key,
        payload::jsonb->>'state' AS state,seq,ts
 FROM agent_events WHERE kind='grant_authority'
), latest AS (
 SELECT DISTINCT ON (session_id,grant_key) session_id,grant_key,state,ts
 FROM observations ORDER BY session_id,grant_key,seq DESC
), authority AS (
 SELECT DISTINCT ON (session_id,grant_key) session_id,grant_key,state
 FROM observations WHERE state<>'unavailable'
 ORDER BY session_id,grant_key,seq DESC
)
INSERT INTO grant_authority_observations(session_id,grant_key,state,authority,asked_at,unavailable_since)
SELECT l.session_id,l.grant_key,l.state,COALESCE(a.state,''),l.ts,
       CASE WHEN l.state='unavailable' THEN l.ts END
FROM latest l LEFT JOIN authority a USING(session_id,grant_key)
WHERE l.grant_key IS NOT NULL;
