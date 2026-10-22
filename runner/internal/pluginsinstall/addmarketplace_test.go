package pluginsinstall

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
)

func TestAddMarketplaceFixedArgvAndAllowList(t *testing.T) {
	allow, _ := pluginspec.ParseAllowlist("example-org/plugins")
	var calls []string
	o := Options{Bin: "claude", Allow: allow, Run: func(_ context.Context, name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	}}
	if err := AddMarketplace(context.Background(), o, "Example-Org/plugins"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "claude plugin marketplace add Example-Org/plugins" {
		t.Fatalf("argv %v", calls)
	}
	for _, bad := range []string{"other/repo", "-x/y", "../x", "https://github.com/example-org/plugins", "example-org/plugins --scope", ""} {
		if err := AddMarketplace(context.Background(), o, bad); !errors.Is(err, ErrNotAllowed) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("a refused source reached the CLI: %v", calls)
	}
	o.Bin = "codex"
	if err := AddMarketplace(context.Background(), o, "example-org/plugins"); err != nil || calls[1] != "codex plugin marketplace add example-org/plugins" {
		t.Fatalf("codex argv %v %v", calls, err)
	}
}
