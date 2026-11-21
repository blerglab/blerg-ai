package server

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestClusterPodControllerIsolationAndBootstrap(t *testing.T) {
	pod := map[string]any{}
	container := map[string]any{"image": "fixture-image", "volumeMounts": []map[string]any{{"name": "projects", "mountPath": "/workspace/projects"}}}
	isolatedClusterPod(pod, container)
	raw, _ := json.Marshal(map[string]any{"pod": pod, "container": container})
	text := string(raw)
	for _, required := range []string{`"mountPath":"/run/blerg-workspace-private"`, `"mountPath":"/runtime-private"`, `chmod 0700 /runtime-private`, `chmod 1777 /runtime-tmp`, `"mountPath":"/runtime-tmp"`, `--reuid=1001`, `--regid=1000`, `--ambient-caps=+chown,+dac_override,+setuid,+setgid,+kill,+sys_ptrace`, `"readOnlyRootFilesystem":true`, `"KILL"`, `"SYS_PTRACE"`, `"name":"workspace-storage"`} {
		if !strings.Contains(text, required) {
			t.Fatalf("production pod missing %s", required)
		}
	}
	for _, forbidden := range []string{`"privileged":true`, `"hostPID":true`, `"hostPath"`, `serviceAccountName`, `secretKeyRef`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("isolation acquired unrelated authority: %s", forbidden)
		}
	}
}

func TestClusterEntrypointsInstallProductionAdapters(t *testing.T) {
	for path, call := range map[string]string{"../../cmd/server/main.go": "api.ConfigureClusterWorkspaces()", "../../cmd/runner/main.go": "runner.ConfigureClusterRuntime(&cfg)"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), call) {
			t.Fatalf("production entry point %s has no adapter", path)
		}
	}
}

func TestClusterAttachmentPathHasWritablePrivateVolume(t *testing.T) {
	f := &fakeK8s{}
	j := newTestJobManager(t, f)
	if err := j.CreateSessionJob(SessionJobSpec{SessionID: "attachments-session", NoRepo: true}); err != nil {
		t.Fatal(err)
	}
	pod := f.created[0]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	// Apply the same production isolation used by workspace pods.
	containers := pod["containers"].([]any)
	container := containers[0].(map[string]any)
	isolatedClusterPod(pod, container)
	var path string
	for _, value := range container["env"].([]any) {
		env := value.(map[string]any)
		if env["name"] == "BLERG_RUNNER_ATTACHMENTS_DIR" {
			path, _ = env["value"].(string)
		}
	}
	if path == "" || strings.HasPrefix(path, "/workspace/") {
		t.Fatalf("non-private path: %q", path)
	}
	for _, mount := range container["volumeMounts"].([]map[string]any) {
		base := mount["mountPath"].(string)
		if path != base && !strings.HasPrefix(path, base+"/") {
			continue
		}
		if mount["readOnly"] == true {
			t.Fatal("attachment mount is read-only")
		}
		var ephemeral bool
		for _, volume := range pod["volumes"].([]map[string]any) {
			if volume["name"] == mount["name"] && volume["emptyDir"] != nil {
				ephemeral = true
			}
		}
		if !ephemeral {
			t.Fatal("attachments are not ephemeral")
		}
		for _, init := range pod["initContainers"].([]map[string]any) {
			for _, initMount := range init["volumeMounts"].([]map[string]any) {
				if initMount["name"] != mount["name"] {
					continue
				}
				script := strings.Join(init["command"].([]string), " ")
				target := initMount["mountPath"].(string)
				if strings.Contains(script, "chown 1000:1000 "+target) && strings.Contains(script, "chmod 0700 "+target) {
					return
				}
			}
		}
		t.Fatal("attachment volume lacks private agent ownership initialization")
	}
	t.Fatalf("attachment path %s has no writable volume (root filesystem is read-only)", filepath.Clean(path))
}

func TestWorkspaceCacheInitRefusesRetainedSymlinks(t *testing.T) {
	for _, name := range []string{".", "go-build", "go-mod", "npm", "tmp", "playwright"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			cache := filepath.Join(root, ".blerg-build-cache")
			target := filepath.Join(root, "private")
			if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
			if name != "." {
				if err := os.Mkdir(cache, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, filepath.Join(cache, name)); err != nil {
				t.Fatal(err)
			}
			script := "chown() { :; }\n" + strings.ReplaceAll(workspaceCacheInitScript, "/runtime-projects", root)
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-ec", script)
			if err := cmd.Run(); err == nil {
				t.Fatal("retained cache symlink accepted by privileged init")
			}
			st, err := os.Stat(target)
			if err != nil || st.Mode().Perm() != 0700 {
				t.Fatalf("private directory permissions changed: %v %v", st, err)
			}
		})
	}
}

func TestWorkspaceCacheTrimDecision(t *testing.T) {
	for _, tc := range []struct {
		name        string
		size, limit int
	}{{"go-build", 1048576, 1048576}, {"go-mod", 1572864, 1572864}, {"npm", 524288, 524288}} {
		for _, over := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/over=%t", tc.name, over), func(t *testing.T) {
				root := t.TempDir()
				cache := filepath.Join(root, ".blerg-build-cache")
				for _, name := range []string{"go-build", "go-mod", "npm", "tmp"} {
					if err := os.MkdirAll(filepath.Join(cache, name), 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(cache, name, "keep"), []byte("fixture"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				size := tc.size
				if over {
					size++
				}
				script := fmt.Sprintf("chown() { :; }\ndu() { case \"$2\" in */%s) echo '%d fixture';; *) echo '1 fixture';; esac; }\n", tc.name, size) + strings.ReplaceAll(workspaceCacheInitScript, "/runtime-projects", root)
				if out, err := exec.CommandContext(t.Context(), "/bin/sh", "-ec", script).CombinedOutput(); err != nil {
					t.Fatalf("init: %v %s", err, out)
				}
				_, err := os.Stat(filepath.Join(cache, tc.name, "keep"))
				if over != os.IsNotExist(err) {
					t.Fatalf("trim decision over=%t: %v", over, err)
				}
				if _, err := os.Stat(filepath.Join(cache, "tmp", "keep")); !os.IsNotExist(err) {
					t.Fatalf("stale compiler scratch retained: %v", err)
				}
			})
		}
	}
}

func TestWorkspaceCacheInitRepairsPlainFiles(t *testing.T) {
	for _, name := range []string{".", "go-build", "go-mod", "npm", "tmp", "playwright"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			cache := filepath.Join(root, ".blerg-build-cache")
			if name != "." {
				if err := os.Mkdir(cache, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(cache, name), []byte("stale"), 0600); err != nil {
				t.Fatal(err)
			}
			script := "chown() { :; }\n" + strings.ReplaceAll(workspaceCacheInitScript, "/runtime-projects", root)
			if out, err := exec.CommandContext(t.Context(), "/bin/sh", "-ec", script).CombinedOutput(); err != nil {
				t.Fatalf("init: %v %s", err, out)
			}
			for _, child := range []string{"go-build", "go-mod", "npm", "tmp"} {
				if st, err := os.Stat(filepath.Join(cache, child)); err != nil || !st.IsDir() {
					t.Fatalf("cache directory %s: %v", child, err)
				}
			}
		})
	}
}
