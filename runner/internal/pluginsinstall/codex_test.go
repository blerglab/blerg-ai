package pluginsinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestCodexRemoteInstallUsesNativeSelectorWithoutGitMarketplace(t *testing.T) {
	allow, _ := pluginspec.ParseAllowlist("codex:openai-curated")
	var commands []string
	res := InstallCodex(context.Background(), Options{Allow: allow, Run: func(_ context.Context, name string, args ...string) (string, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)
		return `{"pluginId":"selected@openai-curated","name":"selected","marketplaceName":"openai-curated","installedPath":"/cache/plugins/cache/openai-curated/selected/1"}`, nil
	}}, []pluginspec.Entry{{Marketplace: "codex:openai-curated", Plugin: "selected"}, {Marketplace: "codex:other", Plugin: "refused"}})
	if res.Installed != 1 || len(res.Skipped) != 1 || len(commands) != 1 || commands[0] != "codex plugin add selected@openai-curated --json" || res.Names["codex:openai-curated"] != "openai-curated" {
		t.Fatalf("result %+v commands %v", res, commands)
	}
}

func TestCodexNativeInstallerRejectsBadMetadataAndFailure(t *testing.T) {
	for _, mode := range []string{"error", "wrong-id", "wrong-market", "missing-path", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			allow, _ := pluginspec.ParseAllowlist("codex:market")
			res := InstallCodex(t.Context(), Options{Allow: allow, PrivateOutput: true, Run: func(context.Context, string, ...string) (string, error) {
				switch mode {
				case "error":
					return "private native error", errors.New("failed")
				case "wrong-id":
					return `{"pluginId":"other@market","name":"selected","marketplaceName":"market","installedPath":"/cache"}`, nil
				case "wrong-market":
					return `{"pluginId":"selected@market","name":"selected","marketplaceName":"other","installedPath":"/cache"}`, nil
				case "missing-path":
					return `{"pluginId":"selected@market","name":"selected","marketplaceName":"market"}`, nil
				default:
					return "not JSON", nil
				}
			}}, []pluginspec.Entry{{Marketplace: "codex:market", Plugin: "selected"}})
			if res.Installed != 0 || len(res.Failed) != 1 || len(res.Paths) != 0 {
				t.Fatalf("invalid native install accepted: %+v", res)
			}
		})
	}
}

func TestCodexNativeInstallerDeadline(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 30\n"), 0755); err != nil {
		t.Fatal(err)
	}
	allow, _ := pluginspec.ParseAllowlist("codex:market")
	res := InstallCodex(t.Context(), Options{Bin: bin, Env: ChildEnv(t.TempDir()), Allow: allow, CmdTimeout: 20 * time.Millisecond, PrivateOutput: true}, []pluginspec.Entry{{Marketplace: "codex:market", Plugin: "selected"}})
	if res.Installed != 0 || len(res.Failed) != 1 {
		t.Fatal("native deadline did not fail closed")
	}
}

func TestCodexMarketplaceListIdentifiesOnlyBuiltinNativeSource(t *testing.T) {
	list, err := ListCodexMarketplaces(t.Context(), Options{Run: func(context.Context, string, ...string) (string, error) {
		return `{"marketplaces":[{"name":"openai-curated","root":"/home/plugins","marketplaceSource":{"sourceType":"local","source":"/home/plugins"}},{"name":"arbitrary","root":"/other/plugins","marketplaceSource":{"sourceType":"local","source":"/other/plugins"}}]}`, nil
	}}, 10)
	if err != nil || len(list) != 2 || list[0].Source != "codex:openai-curated" || list[1].Source != "" {
		t.Fatalf("native built-in identification %+v %v", list, err)
	}
}

func TestCodexNativeMarketplaceCannotBeShadowedByGitManifest(t *testing.T) {
	allow, _ := pluginspec.ParseAllowlist("example-org/plugins,codex:market")
	installs := 0
	res := InstallCodex(t.Context(), Options{Allow: allow, Run: func(_ context.Context, _ string, args ...string) (string, error) {
		if strings.Join(args, " ") == "plugin marketplace list --json" {
			return `{"marketplaces":[{"name":"market","marketplaceSource":{"sourceType":"git","source":"example-org/plugins"}}]}`, nil
		}
		if len(args) > 1 && args[1] == "add" {
			installs++
		}
		return "", nil
	}}, []pluginspec.Entry{{Marketplace: "example-org/plugins", Plugin: "local"}, {Marketplace: "codex:market", Plugin: "selected"}})
	if installs != 0 || res.Installed != 0 || len(res.Failed) != 2 {
		t.Fatalf("native source shadowed: %+v installs %d", res, installs)
	}
}
