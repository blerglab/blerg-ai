package pluginspec

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMarketplacePolicyDefaultsAndExplicitDenyAll(t *testing.T) {
	a, _ := ParseAllowlist("")
	p := PolicyFromAllowlist(a)
	for _, source := range DefaultSources() {
		if !p.Allowlist().Allows(source) {
			t.Fatal("missing official source", source)
		}
	}
	for i := range p.Marketplaces {
		p.Marketplaces[i].Enabled = false
	}
	if err := p.Normalize(); err != nil {
		t.Fatal(err)
	}
	for _, source := range DefaultSources() {
		if p.Allowlist().Allows(source) {
			t.Fatal("empty saved policy restored defaults")
		}
	}
	p.Marketplaces = p.Marketplaces[1:]
	if p.Normalize() == nil {
		t.Fatal("default removed")
	}
}
func TestMarketplacePolicyValidation(t *testing.T) {
	for _, source := range []string{"../escape", "https://example.com/plugins", "codex:UPPER", "codex:-flag", "*"} {
		a, _ := ParseAllowlist("")
		p := PolicyFromAllowlist(a)
		p.Marketplaces = append(p.Marketplaces, Marketplace{Source: source, Enabled: true})
		if p.Normalize() == nil {
			t.Fatal("accepted", source)
		}
	}
	a, _ := ParseAllowlist("")
	p := PolicyFromAllowlist(a)
	p.Marketplaces = append(p.Marketplaces, Marketplace{Source: "Anthropics/Claude-Plugins-Official"})
	if p.Normalize() == nil {
		t.Fatal("case-insensitive duplicate accepted")
	}
}
func TestLiveMarketplacePolicyOverridesEnvironmentAndRetainsAuthority(t *testing.T) {
	fallback, _ := ParseAllowlist("example-org/old")
	a, _ := ParseAllowlist("")
	p := PolicyFromAllowlist(a)
	live := &LivePolicy{}
	if err := live.Apply(p, time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	if live.Allowlist(fallback).Allows("example-org/old") || !live.Allowlist(fallback).Allows("codex:openai-curated-remote") {
		t.Fatal("Core must replace environment, not intersect it")
	}
	p.Marketplaces[0].Source = "changed/value"
	if !live.Allowlist(fallback).Allows(DefaultSources()[0]) {
		t.Fatal("caller mutated saved lease")
	}
	if live.Allowlist(fallback).Allows("example-org/old") {
		t.Fatal("expired authority restored a broader fallback")
	}
	p = PolicyFromAllowlist(a)
	p.Version = 99
	if live.Apply(p, time.Second) == nil {
		t.Fatal("bad wire version accepted")
	}
}

func TestLiveMarketplacePolicyRejectsStaleAndReplayedRevisions(t *testing.T) {
	fallback, _ := ParseAllowlist("*")
	enabled := PolicyFromAllowlist(fallback)
	disabled := PolicyFromAllowlist(Allowlist{})
	disabled.Revision = 3
	live := &LivePolicy{}
	if err := live.Apply(disabled, time.Second); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []int64{1, 3, 2} {
		enabled.Revision = revision
		if err := live.Apply(enabled, time.Minute); err == nil {
			t.Errorf("accepted stale/replayed revision %d", revision)
		}
		if live.Allowlist(fallback).Allows(DefaultSources()[0]) {
			t.Fatalf("revision %d restored revoked permission", revision)
		}
	}
	if live.Allowlist(fallback).Allows(DefaultSources()[0]) {
		t.Fatal("Core unavailable restored revoked permission")
	}
	p, ok := live.Snapshot()
	if !ok || p.Revision != 3 {
		t.Fatal("lost revision high-water mark", p.Revision, ok)
	}
	enabled.Revision = 4
	if err := live.Apply(enabled, time.Second); err != nil {
		t.Fatal(err)
	}
	if !live.Allowlist(fallback).Allows(DefaultSources()[0]) {
		t.Fatal("newer policy could not enable marketplace")
	}
}

func TestLiveMarketplacePolicyPersistsRevisionAndRevocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	live, err := NewLivePolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	fallback, _ := ParseAllowlist("*")
	if !live.Allowlist(fallback).Allows(DefaultSources()[0]) {
		t.Fatal("missing initial fallback")
	}
	disabled := PolicyFromAllowlist(Allowlist{})
	disabled.Revision = 9
	if err := live.Apply(disabled, time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	restored, err := NewLivePolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, holder := range []*LivePolicy{live, restored} {
		if holder.Allowlist(fallback).Allows(DefaultSources()[0]) {
			t.Fatal("expiry/restart restored revoked permission")
		}
		enabled := PolicyFromAllowlist(fallback)
		for _, rev := range []int64{8, 9, 1} {
			enabled.Revision = rev
			if err := holder.Apply(enabled, time.Second); !errors.Is(err, ErrPolicyRevision) {
				t.Fatalf("restored holder accepted revision %d: %v", rev, err)
			}
		}
		p, ok := holder.Snapshot()
		if !ok || p.Revision != 9 {
			t.Fatal("lost persisted revision", p, ok)
		}
	}
}

func TestLiveMarketplacePolicyFailsClosedOnCorruptState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	fallback, _ := ParseAllowlist("*")
	for _, raw := range []string{`{`, `{}`, `{"version":1,"revision":2,"marketplaces":[]}`} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		live, err := NewLivePolicy(path)
		if err == nil {
			t.Fatal("corrupt state loaded")
		}
		if live.Allowlist(fallback).Allows(DefaultSources()[0]) {
			t.Fatal("corrupt cache used permissive fallback")
		}
		if err := live.Apply(PolicyFromAllowlist(fallback), time.Second); err == nil {
			t.Fatal("unknown high-water mark overwritten")
		}
	}
}

func TestLiveMarketplacePolicyDoesNotPublishFailedPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "policy.json")
	live, err := NewLivePolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := live.Apply(PolicyFromAllowlist(Allowlist{}), time.Second); err != nil {
		t.Fatal(err)
	}
	// Replace the directory with a file to force writes to fail even under root.
	if err := os.Rename(filepath.Dir(path), filepath.Dir(path)+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Dir(path), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	fallback, _ := ParseAllowlist("*")
	enabled := PolicyFromAllowlist(fallback)
	enabled.Revision = 2
	if err := live.Apply(enabled, time.Second); err == nil {
		t.Fatal("accepted undurable update")
	}
	if live.Allowlist(fallback).Allows(DefaultSources()[0]) {
		t.Fatal("published undurable permission")
	}
	p, _ := live.Snapshot()
	if p.Revision != 1 {
		t.Fatal("failed write advanced high-water mark", p)
	}
}

func TestLiveMarketplacePolicyConcurrentOutOfOrder(t *testing.T) {
	live, err := NewLivePolicy(filepath.Join(t.TempDir(), "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for revision := int64(1); revision <= 20; revision++ {
		wg.Go(func() {
			p := PolicyFromAllowlist(Allowlist{})
			p.Revision = revision
			err := live.Apply(p, time.Second)
			if err != nil && !errors.Is(err, ErrPolicyRevision) {
				t.Error(err)
			}
			_ = live.Allowlist(Allowlist{})
		})
	}
	wg.Wait()
	p, ok := live.Snapshot()
	if !ok || p.Revision != 20 {
		t.Fatal("out-of-order delivery lost newest revision", p, ok)
	}
	restored, err := NewLivePolicy(live.path)
	if err != nil {
		t.Fatal(err)
	}
	p, _ = restored.Snapshot()
	if p.Revision != 20 {
		t.Fatal("disk lost newest revision", p)
	}
}

func TestLiveMarketplacePolicySharedStateRejectsOlderWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	first, err := NewLivePolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewLivePolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	disabled := PolicyFromAllowlist(Allowlist{})
	disabled.Revision = 3
	if err := second.Apply(disabled, time.Second); err != nil {
		t.Fatal(err)
	}
	allow, _ := ParseAllowlist("*")
	enabled := PolicyFromAllowlist(allow)
	enabled.Revision = 2
	if err := first.Apply(enabled, time.Second); !errors.Is(err, ErrPolicyRevision) {
		t.Fatal("older writer replaced disk policy", err)
	}
	if first.Allowlist(allow).Allows(DefaultSources()[0]) {
		t.Fatal("older writer ignored saved revoke")
	}
	restored, err := NewLivePolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := restored.Snapshot()
	if p.Revision != 3 || restored.Allowlist(allow).Allows(DefaultSources()[0]) {
		t.Fatal("shared state rolled back", p)
	}
}

func TestLiveMarketplacePolicySharedStateAcceptsRevisionOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	var holders []*LivePolicy
	for range 20 {
		holder, err := NewLivePolicy(path)
		if err != nil {
			t.Fatal(err)
		}
		holders = append(holders, holder)
	}
	start := make(chan struct{})
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for _, holder := range holders {
		wg.Go(func() {
			<-start
			p := PolicyFromAllowlist(Allowlist{})
			p.Revision = 9
			err := holder.Apply(p, time.Second)
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, ErrPolicyRevision) {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatal("multiple writers accepted the same revision", accepted.Load())
	}
	for _, holder := range holders {
		p, ok := holder.Snapshot()
		if !ok || p.Revision != 9 {
			t.Fatal("writer did not retain shared high-water mark", p, ok)
		}
	}
}

func TestLiveMarketplacePolicyRestoreThenRevoke(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	first, err := NewLivePolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	allow, _ := ParseAllowlist("*")
	old := PolicyFromAllowlist(allow)
	old.PolicyID, old.Revision = "00000000-0000-4000-8000-000000000001", 7
	if err := first.Apply(old, time.Second); err != nil {
		t.Fatal(err)
	}
	// A second replica already holds the pre-restore policy in memory.
	second, err := NewLivePolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	revoke := PolicyFromAllowlist(Allowlist{})
	revoke.PolicyID, revoke.Revision = "00000000-0000-4000-8000-000000000002", 4
	if err := first.Apply(revoke, time.Second); err != nil {
		t.Fatal("restore froze revocation", err)
	}
	if err := second.Apply(revoke, time.Second); !errors.Is(err, ErrPolicyRevision) {
		t.Fatal("shared replica did not adopt reset mark", err)
	}
	restarted, err := NewLivePolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, holder := range []*LivePolicy{first, second, restarted} {
		p, ok := holder.Snapshot()
		if !ok || p.PolicyID != revoke.PolicyID || p.Revision != 4 || holder.Allowlist(allow).Allows(DefaultSources()[0]) {
			t.Fatal("lost restored authority", p)
		}
		stale := revoke
		stale.Revision = 3
		if err := holder.Apply(stale, time.Second); !errors.Is(err, ErrPolicyRevision) {
			t.Fatal("new ID lost revision protection", err)
		}
		legacy := old
		legacy.PolicyID = ""
		legacy.Revision = 8
		if err := holder.Apply(legacy, time.Second); err == nil {
			t.Fatal("legacy frame replaced identified authority")
		}
	}
}

func TestMarketplacePolicyRejectionDiagnostics(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	live := &LivePolicy{}
	p := PolicyFromAllowlist(Allowlist{})
	p.Revision = 7
	if err := live.Apply(p, time.Second); err != nil {
		t.Fatal(err)
	}
	live.LogApplyError(p, live.Apply(p, time.Second))
	if output.Len() != 0 {
		t.Fatal("unchanged poll logged")
	}
	p.Revision = 4
	live.LogApplyError(p, live.Apply(p, time.Second))
	if !strings.Contains(output.String(), "incoming revision=4 saved revision=7") {
		t.Fatal("stale rejection lost", output.String())
	}
	live.LogApplyError(p, live.Apply(p, time.Second))
	if strings.Count(output.String(), "marketplace policy rejected") != 1 {
		t.Fatal("repeated rejection flooded logs", output.String())
	}
	live.LogApplyError(p, errors.New("policy state cannot be persisted"))
	if !strings.Contains(output.String(), "policy state cannot be persisted") {
		t.Fatal("earlier rejection hid a new persistence error", output.String())
	}
}

func TestMarketplacePolicyIDValidationAndLegacyUpgrade(t *testing.T) {
	live := &LivePolicy{}
	p := PolicyFromAllowlist(Allowlist{})
	if err := live.Apply(p, time.Second); err != nil {
		t.Fatal("legacy upgrade blocked", err)
	}
	for _, id := range []string{"not-a-uuid", "00000000-0000-4000-8000-00000000000z"} {
		p.PolicyID = id
		if err := live.Apply(p, time.Second); err == nil {
			t.Fatal("invalid ID accepted", id)
		}
	}
	p.PolicyID = "00000000-0000-4000-8000-000000000001"
	if err := live.Apply(p, time.Second); err != nil {
		t.Fatal("new ID did not replace legacy revision", err)
	}
}
