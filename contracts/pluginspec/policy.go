package pluginspec

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const MaxMarketplaces = 100

// Marketplace retains disabled entries, including the built-in defaults.
type Marketplace struct {
	Source  string `json:"source"`
	Enabled bool   `json:"enabled"`
}

// Policy is install-wide configuration, never a plugin install/uninstall request.
type Policy struct {
	PolicyID       string        `json:"policy_id"`
	Version        int           `json:"version"`
	Revision       int64         `json:"revision"`
	Marketplaces   []Marketplace `json:"marketplaces"`
	AnyMarketplace bool          `json:"any_marketplace"`
	UpdatedBy      string        `json:"updated_by,omitempty"`
	UpdatedAt      time.Time     `json:"updated_at,omitzero"`
}

func DefaultSources() []string { return strings.Split(DefaultMarketplaces, ",") }

// Normalize validates the complete list. Defaults may be disabled but never removed.
func (p *Policy) Normalize() error {
	if p.Marketplaces == nil || len(p.Marketplaces) > MaxMarketplaces {
		return errors.New("provide at most 100 marketplaces, including the defaults")
	}
	seen := map[string]bool{}
	for i := range p.Marketplaces {
		m := &p.Marketplaces[i]
		m.Source = strings.TrimSpace(m.Source)
		if !ValidSource(m.Source) || seen[strings.ToLower(m.Source)] {
			return errors.New("marketplaces must be unique owner/repo or codex:<marketplace> sources")
		}
		m.Source = strings.ToLower(m.Source)
		seen[m.Source] = true
	}
	for _, d := range DefaultSources() {
		if !seen[d] {
			return errors.New("default marketplaces may be turned off but cannot be removed")
		}
	}
	sort.Slice(p.Marketplaces, func(i, j int) bool { return p.Marketplaces[i].Source < p.Marketplaces[j].Source })
	return nil
}

func PolicyFromAllowlist(a Allowlist) Policy {
	sources, anySource := a.List()
	seen := map[string]bool{}
	for _, s := range sources {
		seen[s] = true
	}
	for _, s := range DefaultSources() {
		seen[s] = true
	}
	p := Policy{Version: 1, Revision: 1, AnyMarketplace: anySource, Marketplaces: []Marketplace{}}
	for s := range seen {
		p.Marketplaces = append(p.Marketplaces, Marketplace{Source: s, Enabled: a.Allows(s)})
	}
	_ = p.Normalize()
	return p
}

func (p Policy) Allowlist() Allowlist {
	// A comma is explicitly empty. An empty environment string means the defaults.
	raw := ","
	if p.AnyMarketplace {
		raw += "*,"
	}
	for _, m := range p.Marketplaces {
		if m.Enabled {
			raw += m.Source + ","
		}
	}
	a, _ := ParseAllowlist(raw)
	return a
}

// LivePolicy retains Core's last accepted policy and revision across outages.
// The zero value is memory-only; production holders use NewLivePolicy with a
// persistent file in their own state directory.
type LivePolicy struct {
	mu                 sync.RWMutex
	policy             Policy
	path               string
	loadErr            error
	lastRejection      time.Time
	lastRejectionError string
}

var ErrPolicyRevision = errors.New("marketplace policy revision is not newer")

// NewLivePolicy restores the local high-water mark before accepting deliveries.
// An unreadable or invalid cache fails closed, including before Core reconnects.
func NewLivePolicy(path string) (*LivePolicy, error) {
	s := &LivePolicy{path: path}
	raw, err := os.ReadFile(path) //nolint:gosec // G304: trusted holder state path, never request input.
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err == nil {
		err = json.Unmarshal(raw, &s.policy)
		if err == nil {
			err = validatePolicy(&s.policy)
		}
	}
	if err != nil {
		s.loadErr = errors.New("cannot restore marketplace policy state")
		return s, s.loadErr
	}
	return s, nil
}

func validPolicyID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func validatePolicy(p *Policy) error {
	if p.PolicyID != "" && !validPolicyID(p.PolicyID) {
		return errors.New("invalid marketplace policy ID")
	}
	if p.Version != 1 || p.Revision < 1 {
		return errors.New("invalid marketplace policy version or revision")
	}
	return p.Normalize()
}

// Apply accepts a new Core policy ID or a strictly newer revision within it.
// Persist before publishing it to readers; reject stale revisions within an ID.
// TTL is validated for v1 wire compatibility, but expiration never restores
// revoked permissions.
func (s *LivePolicy) Apply(p Policy, ttl time.Duration) error {
	if ttl <= 0 || ttl > time.Minute {
		return errors.New("invalid marketplace policy lease")
	}
	p.Marketplaces = append([]Marketplace(nil), p.Marketplaces...)
	if err := validatePolicy(&p); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return s.loadErr
	}
	if s.path != "" {
		unlock, err := lockPolicy(s.path)
		if err != nil {
			return err
		}
		defer unlock()
		// Rolling server replicas can share a data volume. Recheck the disk
		// identity and revision under its lock before comparing the update.
		saved, err := NewLivePolicy(s.path)
		if err != nil {
			return err
		}
		if saved.policy.PolicyID != s.policy.PolicyID || saved.policy.Revision > s.policy.Revision {
			s.policy = saved.policy
		}
	}
	if s.policy.PolicyID != "" && p.PolicyID == "" {
		return errors.New("marketplace policy ID is missing")
	}
	if p.PolicyID == s.policy.PolicyID && p.Revision <= s.policy.Revision {
		return ErrPolicyRevision
	}
	if s.path != "" {
		if err := savePolicy(s.path, p); err != nil {
			return err
		}
	}
	s.policy = p
	return nil
}

// LogApplyError suppresses ordinary unchanged deliveries and bounds repeated
// identical rejection/persistence diagnostics to one per minute per holder.
// A different error is logged immediately, even after a stale-frame warning.
func (s *LivePolicy) LogApplyError(p Policy, err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if errors.Is(err, ErrPolicyRevision) && p.PolicyID == s.policy.PolicyID && p.Revision == s.policy.Revision {
		return
	}
	now := time.Now()
	if err.Error() == s.lastRejectionError && !s.lastRejection.IsZero() && now.Sub(s.lastRejection) < time.Minute {
		return
	}
	s.lastRejection = now
	s.lastRejectionError = err.Error()
	log.Printf("marketplace policy rejected: incoming revision=%d saved revision=%d: %v", p.Revision, s.policy.Revision, err)
}

func (s *LivePolicy) Snapshot() (Policy, bool) {
	if s == nil {
		return Policy{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p := s.policy
	p.Marketplaces = append([]Marketplace(nil), p.Marketplaces...)
	return p, p.Revision > 0 && s.loadErr == nil
}
func (s *LivePolicy) Allowlist(fallback Allowlist) Allowlist {
	if s == nil {
		return fallback
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.loadErr != nil {
		return Allowlist{}
	}
	if s.policy.Revision > 0 {
		return s.policy.Allowlist()
	}
	return fallback
}

func savePolicy(path string, p Policy) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".marketplace-policy-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	err = json.NewEncoder(f).Encode(p)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir) //nolint:gosec // G304: trusted holder state directory, never request input.
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
