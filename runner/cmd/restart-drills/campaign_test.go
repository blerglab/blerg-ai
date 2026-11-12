package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func proof() object {
	return object{"same_session": true, "durable_prefix": true, "marker_retained": true, "one_commit": true, "one_successful_turn": true, "writer_lock": true}
}
func TestCampaignThroughKubernetesTransport(t *testing.T) {
	for _, fail := range []string{"", "fault", "assertions", "regression"} {
		t.Run("failure-"+fail, func(t *testing.T) {
			c := testConfig(t)
			c.State = t.TempDir()
			j := journal{Run: "owned", UID: "ns"}
			current := ""
			killed := false
			boot := "first"
			var trace []string
			patches := 0
			k := func(args []string, _ []byte) ([]byte, error) {
				trace = append(trace, strings.Join(args, " "))
				switch args[0] {
				case "rollout":
					return nil, nil
				case "get":
					if args[1] == "pods" {
						return encode(object{"items": []any{object{"metadata": object{"uid": fmt.Sprintf("pod-%d", patches), "labels": object{label: j.Run}}, "status": object{"containerStatuses": []any{object{"name": "core", "imageID": "digest:a"}, object{"name": "board", "imageID": "digest:b"}, object{"name": "blerg-runner", "imageID": fmt.Sprintf("digest:c%d", patches)}, object{"name": "daemon", "imageID": fmt.Sprintf("digest:d%d", patches)}}}}}}), nil
					}
					return encode(object{"metadata": object{"uid": "deployment-uid", "resourceVersion": "1", "labels": object{label: j.Run}}, "spec": object{"template": object{"spec": object{"containers": []any{object{"name": "server", "image": "image"}}}}}}), nil
				case "patch":
					if fail == "fault" {
						return nil, errors.New("patch refused")
					}
					patches++
					if args[1] == "statefulset" {
						boot = "upgraded"
					}
					return nil, nil
				case "exec":
					if len(args) > 5 && args[5] == "go" {
						if fail == "regression" {
							return nil, errors.New("regression failed")
						}
						return nil, nil
					}
					action := args[7]
					switch action {
					case "regression":
						return []byte(`{"passed":9}`), nil
					case "begin":
						current = args[8]
						killed = false
						return []byte(`{"ok":true}`), nil
					case "armed":
						return encode(object{"barrier": true, "daemon_boot": boot, "engine": "engine", "session": "session"}), nil
					case "kill":
						killed = true
						boot = "second"
						return []byte(`{"killed":true}`), nil
					case "identity":
						return encode(object{"daemon_boot": boot, "engine": "engine"}), nil
					case "recover":
						if current == "daemon" && !killed {
							t.Fatal("recovery before fault")
						}
						return []byte(`{"ok":true}`), nil
					case "release":
						return []byte(`{"ok":true}`), nil
					case "verify":
						if fail == "assertions" {
							return []byte(`{}`), nil
						}
						return encode(proof()), nil
					}
				}
				return nil, fmt.Errorf("unexpected command %v", args)
			}
			err := campaign(c, j, k)
			if (err == nil) != (fail == "") {
				t.Fatalf("campaign result: %v", err)
			}
			var result campaignResult
			if readJSON(filepath.Join(c.State, "results.json"), &result) != nil {
				t.Fatal("missing result")
			}
			if fail == "" {
				if result.Status != "pass" || len(result.Cases) != 3 || patches != 5 {
					t.Fatalf("incomplete campaign: %+v patches=%d", result, patches)
				}
				if campaign(c, j, k) == nil {
					t.Fatal("reused campaign directory")
				}
			} else if result.Status != "fail" {
				t.Fatal("failure counted as pass")
			}
			if fail == "regression" && (len(result.Cases) != 3 || result.Cases[0].Status != "not-run") {
				t.Fatal("fault ran without prerequisites")
			}
		})
	}
}
func TestMutationRequiresOwnedWorkload(t *testing.T) {
	calls := 0
	err := patchWorkload(func([]string, []byte) ([]byte, error) {
		calls++
		return []byte(`{"metadata":{"uid":"other","resourceVersion":"1","labels":{}}}`), nil
	}, "deployment", "blerg-runner", "owned", object{})
	if err == nil || calls != 1 {
		t.Fatal("mutated unowned workload")
	}
}
func TestEventProof(t *testing.T) {
	before := json.RawMessage(`{"events":[{"seq":1,"kind":"user_message","payload":{"text":"start"}}]}`)
	good := `{"events":[{"seq":1,"kind":"user_message","payload":{"text":"start"}},{"seq":2,"kind":"assistant_text","payload":{"text":"restart-drill-committed","done":true}},{"seq":3,"kind":"turn_done","payload":{"stop_reason":"end_turn"}}]}`
	for _, tc := range []struct {
		name, after string
		ok          bool
	}{{"pass", good, true}, {"missing", "{}", false}, {"changed", strings.Replace(good, "start", "different", 1), false}, {"pagination", strings.Replace(good, `{"events":`, `{"has_more":true,"events":`, 1), false}, {"duplicate", good[:len(good)-2] + `,{"seq":4,"kind":"turn_done","payload":{"stop_reason":"end_turn"}}]}`, false}} {
		t.Run(tc.name, func(t *testing.T) {
			if (eventProof(before, json.RawMessage(tc.after)) == nil) != tc.ok {
				t.Fatal("incorrect event verdict")
			}
		})
	}
}

func TestExecRefusesForeignPod(t *testing.T) {
	calls := 0
	k := ownedExec(func([]string, []byte) ([]byte, error) {
		calls++
		return []byte(`{"metadata":{"uid":"pod","labels":{}}}`), nil
	}, "owned")
	if _, err := k([]string{"exec", "restart-desktop-0", "--", "go"}, nil); err == nil || calls != 1 {
		t.Fatal("executed in foreign pod")
	}
}
func TestReplacementProof(t *testing.T) {
	base := []object{{"container": "blerg-runner", "pod_uid": "old", "image_id": "server-old"}, {"container": "daemon", "pod_uid": "desktop-old", "image_id": "daemon-old"}}
	next := []object{{"container": "blerg-runner", "pod_uid": "new", "image_id": "server-new"}, {"container": "daemon", "pod_uid": "desktop-new", "image_id": "daemon-new"}}
	if replacementProof(base, next, "upgrade") != nil {
		t.Fatal("valid upgrade refused")
	}
	if replacementProof(base, base, "server") == nil || replacementProof(base, base, "upgrade") == nil {
		t.Fatal("no-op accepted")
	}
	if replacementProof(base, append(append([]object{}, next...), base...), "server") == nil {
		t.Fatal("old server still present")
	}
	next[1]["image_id"] = "daemon-old"
	if replacementProof(base, next, "upgrade") == nil {
		t.Fatal("same daemon image accepted")
	}
}
