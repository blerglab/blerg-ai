package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testConfig(t *testing.T) config {
	t.Helper()
	return config{Context: "lab", Namespace: "acceptance-restart-test", Image: "fixture:candidate", Postgres: "postgres:16", State: filepath.Join(t.TempDir(), "state"), PreviousCore: "core:previous", PreviousBoard: "board:previous", PreviousRunner: "runner:previous", PreviousDaemon: "fixture:previous", Core: "core:candidate", Board: "board:candidate", Runner: "runner:candidate", Daemon: "fixture:candidate", CoreHost: "acceptance-restart-test.example.invalid", BoardHost: "acceptance-restart-test-board.example.invalid", RunnerHost: "acceptance-restart-test-runner.example.invalid", IngressClass: "nginx", StorageClass: "disposable", Templates: "../../../install/k8s"}
}
func TestConfig(t *testing.T) {
	env := map[string]string{"BLERG_DRILL_CONTEXT": "lab", "BLERG_DRILL_NAMESPACE": "acceptance-restart-test", "BLERG_DRILL_IMAGE": "fixture:test", "BLERG_DRILL_POSTGRES_IMAGE": "postgres:16", "BLERG_DRILL_STATE": "private"}
	if _, err := load(func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	for key := range env {
		t.Run(key, func(t *testing.T) {
			saved := env[key]
			env[key] = ""
			defer func() { env[key] = saved }()
			if _, err := load(func(k string) string { return env[k] }); err == nil {
				t.Fatal("accepted missing setting")
			}
		})
	}
}
func TestInstallConfigRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*config)
	}{{"shared-host", func(c *config) { c.CoreHost = "example.invalid" }}, {"duplicate", func(c *config) { c.BoardHost = c.CoreHost }}, {"same-version", func(c *config) { c.Runner = c.PreviousRunner }}, {"missing-image", func(c *config) { c.Core = "" }}, {"missing-class", func(c *config) { c.IngressClass = "" }}} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConfig(t)
			tc.edit(&c)
			if c.validateInstall() == nil {
				t.Fatal("accepted unsafe configuration")
			}
		})
	}
}
func TestOwnership(t *testing.T) {
	j := journal{UID: "uid", Run: "run"}
	for _, tc := range []struct {
		uid, run string
		ok       bool
	}{{"uid", "run", true}, {"other", "run", false}, {"uid", "other", false}, {"", "", false}} {
		raw := encode(object{"metadata": object{"uid": tc.uid, "labels": object{label: tc.run}}})
		if (owned(raw, j) == nil) != tc.ok {
			t.Fatalf("ownership check: %+v", tc)
		}
	}
}
func TestPlanNeverCallsCluster(t *testing.T) {
	if err := execute("plan", testConfig(t), func([]string, []byte) ([]byte, error) { t.Fatal("cluster contact"); return nil, nil }); err != nil {
		t.Fatal(err)
	}
}
func TestDownUsesUIDPrecondition(t *testing.T) {
	for _, match := range []bool{true, false} {
		t.Run(map[bool]string{true: "owned", false: "replaced"}[match], func(t *testing.T) {
			dir := t.TempDir()
			c := testConfig(t)
			c.State = dir
			j := journal{Context: c.Context, Namespace: c.Namespace, UID: "uid", Run: "run"}
			if err := save(dir, "owner.json", j); err != nil {
				t.Fatal(err)
			}
			calls := 0
			err := execute("down", c, func(args []string, b []byte) ([]byte, error) {
				calls++
				switch calls {
				case 1:
					uid := "uid"
					if !match {
						uid = "new"
					}
					return encode(object{"metadata": object{"uid": uid, "labels": object{label: "run"}}}), nil
				case 2:
					if len(args) != 5 || args[0] != "delete" || args[1] != "--raw" || args[2] != "/api/v1/namespaces/"+c.Namespace {
						t.Fatalf("unsafe delete: %v", args)
					}
					var options struct{ Preconditions struct{ UID string } }
					if json.Unmarshal(b, &options) != nil || options.Preconditions.UID != "uid" {
						t.Fatal("missing precondition")
					}
				case 3:
					if args[0] != "wait" {
						t.Fatal("must wait for teardown")
					}
				}
				return nil, nil
			})
			if match && (err != nil || calls != 3) {
				t.Fatalf("owned delete: %d %v", calls, err)
			}
			if !match && (err == nil || calls != 1) {
				t.Fatal("deleted replacement")
			}
		})
	}
}
func TestUpNeverAdoptsExistingState(t *testing.T) {
	c := testConfig(t)
	c.State = t.TempDir()
	if err := execute("up", c, func([]string, []byte) ([]byte, error) { t.Fatal("cluster contact"); return nil, nil }); err == nil {
		t.Fatal("adopted existing state")
	}
}
func TestUpJournalsBeforeResources(t *testing.T) {
	c := testConfig(t)
	created := 0
	err := execute("up", c, func(args []string, b []byte) ([]byte, error) {
		switch args[0] {
		case "get":
			if args[1] == "storageclass" {
				return []byte(`{"reclaimPolicy":"Delete"}`), nil
			}
			return []byte(`{"items":[]}`), nil
		case "create":
			var o object
			if err := json.Unmarshal(b, &o); err != nil {
				t.Fatal(err)
			}
			created++
			if o["kind"] == "Namespace" {
				mapAt(o, "metadata")["uid"] = "created-uid"
				return encode(o), nil
			}
			var j journal
			if readJSON(filepath.Join(c.State, "owner.json"), &j) != nil || j.UID != "created-uid" {
				t.Fatal("missing creation journal")
			}
		case "rollout":
		default:
			t.Fatalf("unexpected command %v", args)
		}
		return nil, nil
	})
	if err != nil || created < 10 {
		t.Fatalf("up: %d %v", created, err)
	}
}
func TestInvalidActionNeverContactsCluster(t *testing.T) {
	if err := execute("typo", config{}, func([]string, []byte) ([]byte, error) { t.Fatal("cluster contact"); return nil, nil }); err == nil {
		t.Fatal("accepted invalid action")
	}
}
func TestPreflightRefusesSharedIngressAndRetainStorage(t *testing.T) {
	for _, kind := range []string{"storage", "host", "wildcard", "transport"} {
		t.Run(kind, func(t *testing.T) {
			c := testConfig(t)
			err := preflight(c, func(args []string, _ []byte) ([]byte, error) {
				if kind == "transport" {
					return nil, errors.New("offline")
				}
				if args[1] == "storageclass" {
					policy := "Delete"
					if kind == "storage" {
						policy = "Retain"
					}
					return encode(object{"reclaimPolicy": policy}), nil
				}
				host := c.CoreHost
				if kind == "wildcard" {
					host = "*.example.invalid"
				}
				return encode(object{"items": []any{object{"spec": object{"rules": []any{object{"host": host}}}}}}), nil
			})
			if err == nil {
				t.Fatal("unsafe admission")
			}
		})
	}
}
func TestRenderFullPrivateInstall(t *testing.T) {
	c := testConfig(t)
	items, err := render(c, "owned", false)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var secrets object
	for _, o := range items {
		m := mapAt(o, "metadata")
		if m["namespace"] != c.Namespace || mapAt(m, "labels")[label] != "owned" {
			t.Fatal("foreign metadata")
		}
		kind := o["kind"].(string)
		seen[kind+"/"+m["name"].(string)] = true
		if kind == "Secret" {
			secrets = mapAt(o, "stringData")
		}
		if strings.Contains(string(encode(o)), "${") {
			t.Fatal("unresolved variable")
		}
		if kind == "ClusterRole" || kind == "PersistentVolume" {
			t.Fatal("cluster-scoped resource")
		}
	}
	for _, name := range []string{"Deployment/blerg-core", "Deployment/blerg-board", "Deployment/blerg-runner", "Deployment/restart-postgres", "StatefulSet/restart-desktop", "Ingress/restart-drills", "NetworkPolicy/restart-egress", "PersistentVolumeClaim/blerg-runner-data"} {
		if !seen[name] {
			t.Fatal("missing " + name)
		}
	}
	for _, key := range []string{"DATABASE_URL_CORE", "DATABASE_URL_BOARD", "BLERG_RUNNER_DATABASE_URL"} {
		if !strings.Contains(secrets[key].(string), "@restart-postgres:5432/") {
			t.Fatal("external database")
		}
	}
	if secrets["DRILL_CONTROL_KEY"] == "owned" || secrets["DRILL_CONTROL_KEY"] == secrets["POSTGRES_PASSWORD"] {
		t.Fatal("secrets share public ownership identifier")
	}
}
func TestFailedCreateNeverDeletesOrAdopts(t *testing.T) {
	c := testConfig(t)
	err := execute("up", c, func(args []string, _ []byte) ([]byte, error) {
		if args[0] == "get" {
			if args[1] == "storageclass" {
				return []byte(`{"reclaimPolicy":"Delete"}`), nil
			}
			return []byte(`{"items":[]}`), nil
		}
		if args[0] != "create" {
			t.Fatal("unexpected mutation")
		}
		return nil, errors.New("namespace exists")
	})
	if err == nil {
		t.Fatal("accepted existing namespace")
	}
	raw, err := os.ReadFile(filepath.Join(c.State, "owner.json"))
	if err != nil {
		t.Fatal(err)
	}
	var j journal
	if json.Unmarshal(raw, &j) != nil || j.UID != "" {
		t.Fatal("adopted namespace")
	}
}

func TestRenderedReferencesResolve(t *testing.T) {
	items, err := render(testConfig(t), "owned", false)
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]object{}
	claims := map[string]bool{}
	for _, o := range items {
		m := mapAt(o, "metadata")
		name, _ := m["name"].(string)
		switch o["kind"] {
		case "Secret":
			sources["secret/"+name] = mapAt(o, "stringData")
		case "ConfigMap":
			sources["config/"+name] = mapAt(o, "data")
		case "PersistentVolumeClaim":
			claims[name] = true
		}
	}
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for _, kind := range []struct{ key, prefix string }{{"secretKeyRef", "secret/"}, {"configMapKeyRef", "config/"}} {
				if ref, ok := v[kind.key].(map[string]any); ok && ref["optional"] != true {
					name, _ := ref["name"].(string)
					key, _ := ref["key"].(string)
					if _, ok = sources[kind.prefix+name][key]; !ok {
						t.Errorf("unresolved %s%s/%s", kind.prefix, name, key)
					}
				}
			}
			if claim, ok := v["persistentVolumeClaim"].(map[string]any); ok {
				if name, _ := claim["claimName"].(string); !claims[name] {
					t.Error("unresolved claim")
				}
			}
			for _, nested := range v {
				walk(nested)
			}
		case []any:
			for _, nested := range v {
				walk(nested)
			}
		}
	}
	for _, o := range items {
		walk(o)
	}
}
func TestRunRefusesChangedConfiguration(t *testing.T) {
	c := testConfig(t)
	c.State = t.TempDir()
	j := journal{Context: c.Context, Namespace: c.Namespace, Run: "owned", UID: "uid", ConfigHash: "different"}
	if err := save(c.State, "owner.json", j); err != nil {
		t.Fatal(err)
	}
	calls := 0
	err := execute("run", c, func([]string, []byte) ([]byte, error) {
		calls++
		return encode(object{"metadata": object{"uid": "uid", "labels": object{label: "owned"}}}), nil
	})
	if err == nil || calls != 1 {
		t.Fatal("ran changed configuration")
	}
}
