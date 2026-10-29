package plugins

import (
	"testing"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
)

func TestCodexRegistryPreservesMarketplaceValidation(t *testing.T) {
	if _, ok := Lookup("codex"); !ok {
		t.Fatal("Codex cannot register managed plugins")
	}
	allow, _ := pluginspec.ParseAllowlist("example-org/plugins")
	service := &Service{allow: allow}
	if err := service.Validate("codex", []Entry{{Marketplace: "example-org/plugins", Plugin: "selected"}}); err != nil {
		t.Fatalf("valid selected plugin rejected: %v", err)
	}
	for _, entry := range []Entry{{Marketplace: "other-org/plugins", Plugin: "selected"}, {Marketplace: "example-org/plugins", Plugin: "../escape"}} {
		if err := service.Validate("codex", []Entry{entry}); err == nil {
			t.Fatalf("Codex bypassed plugin validation: %+v", entry)
		}
	}
}

func TestCodexNativeMarketplaceKeepsEngineAndOperatorPolicy(t *testing.T) {
	allow, _ := pluginspec.ParseAllowlist("codex:openai-curated")
	service := &Service{allow: allow}
	e := Entry{Marketplace: "codex:openai-curated", Plugin: "selected"}
	if err := service.Validate("codex", []Entry{e}); err != nil {
		t.Fatal(err)
	}
	if err := service.Validate("claude", []Entry{e}); err == nil {
		t.Fatal("Claude accepted native Codex identity")
	}
	e.Marketplace = "codex:unapproved"
	if err := service.Validate("codex", []Entry{e}); err == nil {
		t.Fatal("native Codex bypassed allowlist")
	}
}
