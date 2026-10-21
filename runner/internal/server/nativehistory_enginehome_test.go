package server

import (
	"errors"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/nativehistory"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/engines"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// engineCredentialFiles are the files each engine's credentials are written
// to inside a session pod (runner.go writes Codex's auth.json and Hermes's
// .env; Claude Code keeps its own login files).
var engineCredentialFiles = []string{
	"/workspace/.codex/auth.json",
	"/workspace/.claude/.credentials.json",
	"/workspace/.claude.json",
	"/workspace/.hermes/.env",
	"/workspace/.hermes/auth.json",
}

func underDir(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }

// coveringMount is the mount whose mountPath is the deepest ancestor of p.
func coveringMount(mounts []map[string]any, p string) map[string]any {
	var best map[string]any
	for _, m := range mounts {
		mp, _ := m["mountPath"].(string)
		if underDir(p, mp) && (best == nil || len(mp) > len(best["mountPath"].(string))) {
			best = m
		}
	}
	return best
}

// A nested mountpoint makes the container runtime create every missing parent
// directory as root. Under the agent's HOME such a parent (e.g. ~/.codex) must
// itself be an agent-owned, pod-ephemeral mount prepared by an init container
// running as the agent, or the engine cannot write its login there.
func TestNativeHistoryPodGivesEachEngineAWritableHome(t *testing.T) {
	for _, engine := range engines.All() {
		if !slices.Contains(engine.Runtimes, "cluster") {
			continue
		}
		t.Run(engine.ID, func(t *testing.T) {
			pod := map[string]any{"containers": []map[string]any{{"image": "fixture-image"}}}
			spec := SessionJobSpec{Engine: engine.ID, nativeHistory: &db.SessionNativeHistory{ClaimName: "fixture-claim"}}
			if err := nativeHistoryPod(spec, pod); err != nil {
				t.Fatal(err)
			}
			mounts := pod["containers"].([]map[string]any)[0]["volumeMounts"].([]map[string]any)
			volumes := map[string]map[string]any{}
			for _, v := range pod["volumes"].([]map[string]any) {
				volumes[v["name"].(string)] = v
			}
			inits, _ := pod["initContainers"].([]map[string]any)
			prepared := func(m map[string]any) bool {
				name, _ := m["name"].(string)
				sub, _ := m["subPath"].(string)
				if volumes[name]["emptyDir"] == nil || sub == "" {
					return false
				}
				for _, ic := range inits {
					sc, _ := ic["securityContext"].(map[string]any)
					if sc["runAsUser"] != sessionAgentUID {
						continue
					}
					for _, im := range ic["volumeMounts"].([]map[string]any) {
						if im["name"] == name && strings.Contains(strings.Join(ic["command"].([]string), " "), path.Join(im["mountPath"].(string), sub)) && strings.Contains(strings.Join(ic["command"].([]string), " "), "umask 077") {
							return true
						}
					}
				}
				return false
			}
			for _, m := range mounts {
				if m["name"] != "native-history" {
					continue
				}
				mp := m["mountPath"].(string)
				if !underDir(mp, sessionHomeDir) {
					continue
				}
				for dir := path.Dir(mp); dir != sessionHomeDir && underDir(dir, sessionHomeDir); dir = path.Dir(dir) {
					parent := coveringMount(mounts, dir)
					if parent == nil || parent["mountPath"] != dir || !prepared(parent) {
						t.Fatalf("%s is created by the container runtime as root above the native-history mount %s; the engine cannot write its home", dir, mp)
					}
				}
			}
			for _, cred := range engineCredentialFiles {
				if m := coveringMount(mounts, cred); m != nil && m["name"] == "native-history" {
					t.Fatalf("credential file %s would land on the durable native-history volume", cred)
				}
			}
		})
	}
}

// A future engine layout must not place retained data directly under HOME or outside its
// ephemeral engine directory. Refusal leaves the pod untouched.
func TestNativeHistoryRefusesUnsafeEngineHome(t *testing.T) {
	for _, layout := range []nativehistory.Layout{
		{Home: "/tmp/.engine"},
		{Home: sessionHomeDir + "/.."},
		{Home: sessionHomeDir + "/../workspace/.engine"},
		{Home: sessionHomeDir + "/."},
		{Home: sessionHomeDir + "/.engine/nested"},
		{Mounts: []nativehistory.Mount{{SubPath: "history", Path: sessionHomeDir + "/history"}}},
		{Home: sessionHomeDir + "/.engine", Mounts: []nativehistory.Mount{{SubPath: "history", Path: sessionHomeDir + "/.other/history"}}},
	} {
		t.Run(layout.Home+fmt.Sprint(layout.Mounts), func(t *testing.T) {
			pod := map[string]any{"containers": []map[string]any{{"image": "fixture-image"}}}
			if err := nativeHistoryPodLayout(SessionJobSpec{nativeHistory: &db.SessionNativeHistory{ClaimName: "fixture-claim"}}, pod, layout); !errors.Is(err, errNativeHistory) {
				t.Fatalf("unsafe layout accepted: %v", err)
			}
			if len(pod) != 1 || len(pod["containers"].([]map[string]any)[0]) != 1 {
				t.Fatal("refused layout changed the pod")
			}
		})
	}
}
