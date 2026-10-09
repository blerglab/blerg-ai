package discovery

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/blerglab/blerg-ai/contracts/agentsmanifest"
	"github.com/blerglab/blerg-ai/core/internal/db"
)

type Registry struct {
	st  db.Store
	ttl time.Duration
}

func NewRegistry(st db.Store, ttl time.Duration) *Registry { return &Registry{st: st, ttl: ttl} }

func (r *Registry) Register(ctx context.Context, e agentsmanifest.ComponentEntry) error {
	caps, err := json.Marshal(e.Capabilities)
	if err != nil {
		return err
	}
	// The whole entry is stored verbatim in `manifest` so the self-describing fields
	// (description, docs/openapi/mcp URLs, auth, operations) survive without a column each;
	// Aggregate merges it back. LastSeen/Stale/Auth.TokenEndpoint are core-filled, so whatever
	// a component sent for them is deliberately dropped here rather than persisted as fact.
	stored := e
	stored.LastSeen, stored.Stale = 0, false
	if stored.Auth != nil {
		auth := *stored.Auth
		auth.TokenEndpoint = ""
		stored.Auth = &auth
	}
	manifest, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	_, err = r.st.Pool().Exec(ctx,
		`INSERT INTO components(name, base_url, version, contract_version, capabilities, manifest, last_seen)
		 VALUES ($1,$2,$3,$4,$5,$6, now())
		 ON CONFLICT (name) DO UPDATE SET
		   base_url=EXCLUDED.base_url, version=EXCLUDED.version,
		   contract_version=EXCLUDED.contract_version, capabilities=EXCLUDED.capabilities,
		   manifest=EXCLUDED.manifest, last_seen=now()`,
		e.Name, e.BaseURL, e.Version, e.ContractVersion, caps, manifest)
	return err
}

func (r *Registry) Aggregate(ctx context.Context) (agentsmanifest.AggregateManifest, error) {
	rows, err := r.st.Pool().Query(ctx,
		`SELECT name, base_url, version, contract_version, capabilities, manifest,
		        extract(epoch from last_seen)::bigint,
		        extract(epoch from (now() - last_seen)) > $1 AS stale
		 FROM components ORDER BY name`, r.ttl.Seconds())
	if err != nil {
		return agentsmanifest.AggregateManifest{}, err
	}
	defer rows.Close()
	var out agentsmanifest.AggregateManifest
	for rows.Next() {
		var e agentsmanifest.ComponentEntry
		var caps, manifest []byte
		if err := rows.Scan(&e.Name, &e.BaseURL, &e.Version, &e.ContractVersion, &caps, &manifest, &e.LastSeen, &e.Stale); err != nil {
			return out, err
		}
		_ = json.Unmarshal(caps, &e.Capabilities)
		// Merge the stored entry back: the dedicated columns stay authoritative for the
		// fields they hold (they are what the ON CONFLICT upsert and the stale check act
		// on), and the JSON supplies only the self-describing fields that have no column.
		var stored agentsmanifest.ComponentEntry
		if err := json.Unmarshal(manifest, &stored); err != nil {
			// The row still lists (its dedicated columns are intact), but the
			// component's self-description is lost — no docs URL, no operations.
			// That is an operator's problem to see, not something to swallow:
			// re-registration fixes it, and nothing else will.
			log.Printf("discovery: component %q has an unreadable manifest: %v", e.Name, err)
		} else {
			e.Description = stored.Description
			e.DocsURL = stored.DocsURL
			e.OpenAPIURL = stored.OpenAPIURL
			e.MCPURL = stored.MCPURL
			e.Auth = stored.Auth
			e.Operations = stored.Operations
			e.UI = stored.UI
		}
		out.Components = append(out.Components, e)
	}
	return out, rows.Err()
}
