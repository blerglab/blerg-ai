package runner

import (
	"context"
	"net/url"
	"time"

	"github.com/blerglab/blerg-ai/contracts/engines"
)

// Typed discovery deliberately excludes credentials and arbitrary upstream
// fields. The board returns this metadata, never the bearer used to request it.
type Model struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Section       string   `json:"section"`
	Efforts       []string `json:"efforts"`
	DefaultEffort string   `json:"default_effort,omitempty"`
	EffortKind    string   `json:"effort_kind,omitempty"`
}
type ModelList struct {
	Refreshing    bool       `json:"refreshing,omitempty"`
	Error         string     `json:"error,omitempty"`
	Models        []Model    `json:"models"`
	Source        string     `json:"source"`
	FetchedAt     *time.Time `json:"fetched_at"`
	DaemonID      string     `json:"daemon_id,omitempty"`
	EngineEfforts []string   `json:"engine_efforts"`
}
type LaunchEngine struct {
	engines.Descriptor
	Models   ModelList `json:"models"`
	Runtime  string    `json:"runtime"`
	DaemonID string    `json:"daemon_id,omitempty"`
}
type LaunchOptions struct {
	State      string         `json:"state"`
	Refreshing bool           `json:"refreshing,omitempty"`
	Runtime    string         `json:"runtime"`
	Engines    []LaunchEngine `json:"engines"`
	Error      string         `json:"error,omitempty"`
}
type Discoverer interface {
	Discover(context.Context, string, string, bool) (LaunchOptions, error)
}

func (k *BlergRunner) Discover(ctx context.Context, token, repo string, refresh bool) (LaunchOptions, error) {
	q := url.Values{"kind": {"agent"}}
	if k.runtime != "" {
		q.Set("runtime", k.runtime)
	}
	if repo != "" {
		q.Set("repo", repo)
	}
	if refresh {
		q.Set("refresh", "1")
	}
	var out LaunchOptions
	_, err := k.doAs(ctx, token, "GET", "/api/runner/launch-options?"+q.Encode(), nil, &out)
	if out.Engines == nil {
		out.Engines = []LaunchEngine{}
	}
	return out, err
}

type SessionDiscoverer interface {
	DiscoverSession(context.Context, string, string, bool) (LaunchOptions, error)
}

func (k *BlergRunner) DiscoverSession(ctx context.Context, token, sessionID string, refresh bool) (LaunchOptions, error) {
	q := url.Values{"session_id": {sessionID}}
	if refresh {
		q.Set("refresh", "1")
	}
	var out LaunchOptions
	_, err := k.doAs(ctx, token, "GET", "/api/runner/launch-options?"+q.Encode(), nil, &out)
	if out.Engines == nil {
		out.Engines = []LaunchEngine{}
	}
	return out, err
}
