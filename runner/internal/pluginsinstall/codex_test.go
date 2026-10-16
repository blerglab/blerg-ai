package pluginsinstall

import (
	"context"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
)

func TestCodexInstallUsesNativeCommandsAndSelectedPlugins(t *testing.T) {
	allow, _ := pluginspec.ParseAllowlist("example-org/plugins")
	var commands []string
	res := InstallCodex(context.Background(), Options{Allow: allow, Run: func(_ context.Context, name string, args ...string) (string, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)
		switch {
		case strings.Contains(command, "marketplace list"):
			return `{"marketplaces":[{"name":"selected-market","marketplaceSource":{"sourceType":"git","source":"https://github.com/example-org/plugins.git"}}]}`, nil
		case strings.Contains(command, "plugin add"):
			return `{"pluginId":"selected@selected-market","name":"selected","marketplaceName":"selected-market","installedPath":"/cache/plugins/cache/selected-market/selected/1.0.0"}`, nil
		default:
			return "", nil
		}
	}}, []pluginspec.Entry{{Marketplace: "example-org/plugins", Plugin: "selected"}, {Marketplace: "not-allowed/plugins", Plugin: "other"}})
	if res.Installed != 1 || len(res.Skipped) != 1 || len(commands) != 3 {
		t.Fatalf("result %+v commands %v", res, commands)
	}
	if commands[0] != "codex plugin marketplace add example-org/plugins" || commands[2] != "codex plugin add selected@selected-market --json" {
		t.Fatalf("unexpected native commands: %v", commands)
	}
	if res.Paths["selected@selected-market"] != "/cache/plugins/cache/selected-market/selected/1.0.0" {
		t.Fatal("lost installed path")
	}
}
