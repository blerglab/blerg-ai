package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// Inspect the entire rendered pod, including init containers and mounted
// Secrets, and resolve every reference to catch delivery before process start.
func assertAccessOnlyPod(t *testing.T, f *fakeK8s) {
	t.Helper()
	if len(f.created) == 0 {
		t.Fatal("missing rendered pod")
	}
	session := createdSessionSecretData(t, f)
	var inspect func(any)
	check := func(value string) {
		if strings.Contains(value, "refresh_token") || strings.Contains(value, "fixture-refresh-private") {
			t.Fatal("pod delivery contains refresh-token material")
		}
	}
	inspect = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if ref, ok := v["secretKeyRef"].(map[string]any); ok {
				name, _ := ref["name"].(string)
				key, _ := ref["key"].(string)
				if name == "blerg-runner-session-s-snapshot" {
					check(session[key])
				} else if _, exists := f.secrets[name][key]; exists && key == "CODEX_AUTH_JSON" {
					t.Fatal("pod still references the original operator bundle")
				}
			}
			if _, ok := v["secret"].(map[string]any); ok {
				t.Fatal("unexpected whole Secret mount")
			}
			if _, ok := v["secretRef"].(map[string]any); ok {
				t.Fatal("unexpected whole Secret environment")
			}
			for k, value := range v {
				check(k)
				inspect(value)
			}
		case []any:
			for _, value := range v {
				inspect(value)
			}
		case string:
			check(v)
		}
	}
	raw, _ := json.Marshal(f.created)
	var pod any
	if json.Unmarshal(raw, &pod) != nil {
		t.Fatal("invalid rendered pod")
	}
	inspect(pod)
	for _, value := range session {
		check(value)
	}
	auth := session["CODEX_AUTH_JSON"]
	if !strings.Contains(auth, "fixture-access-private") {
		t.Fatal("access snapshot missing")
	}
}

func TestCodexSnapshotPodDelivery(t *testing.T) {
	for _, source := range []string{"personal", "caller", "operator", "operator-oauth"} {
		for _, layout := range []string{"scalar", "workspace", "native-history"} {
			t.Run(source+"/"+layout, func(t *testing.T) {
				bundle := []byte(`{"tokens":{"access_token":"fixture-access-private","refresh_token":"fixture-refresh-private","account_id":"fixture-account"},"unknown":{"refresh_token":"fixture-refresh-private"}}`)
				f := &fakeK8s{secrets: map[string]map[string]string{}}
				jm := newTestJobManager(t, f)
				spec := SessionJobSpec{SessionID: "s-snapshot", Engine: "codex", NoRepo: true, PluginsResolved: true}
				switch source {
				case "personal":
					spec.SpawningAccountID, spec.AuthSessionID = "fixture-owner", testSID
					f.credentialFetchResponses = map[string][]byte{"fixture-owner:codex": bundle}
				case "caller":
					spec.ExtraEnv = map[string]string{"CODEX_AUTH_JSON": string(bundle)}
				case "operator":
					f.secrets[jm.SecretName] = map[string]string{"CODEX_AUTH_JSON": b64(string(bundle))}
				case "operator-oauth":
					jm.OAuthSecretName = "operator-oauth"
					f.secrets[jm.OAuthSecretName] = map[string]string{"CODEX_AUTH_JSON": b64(string(bundle))}
				}
				switch layout {
				case "workspace":
					plan := clusterPlanFixture()
					plan.SessionID = spec.SessionID
					spec.workspacePlan, spec.workspaceClaim = &plan, "fixture-workspace"
				case "native-history":
					spec.nativeHistory = &db.SessionNativeHistory{ClaimName: "fixture-history"}
				}
				if err := jm.createSessionJob(spec); err != nil {
					t.Fatal("snapshot launch failed")
				}
				assertAccessOnlyPod(t, f)
			})
		}
	}
}

func TestCodexSnapshotPodRejectsInvalidCredential(t *testing.T) {
	for _, source := range []string{"personal", "caller", "operator"} {
		t.Run(source, func(t *testing.T) {
			invalid := `{"tokens":{"refresh_token":"fixture-refresh-private"}}`
			f := &fakeK8s{}
			jm := newTestJobManager(t, f)
			spec := SessionJobSpec{SessionID: "s-snapshot", Engine: "codex", NoRepo: true, PluginsResolved: true}
			switch source {
			case "personal":
				spec.SpawningAccountID, spec.AuthSessionID = "fixture-owner", testSID
				f.credentialFetchResponses = map[string][]byte{"fixture-owner:codex": []byte(invalid)}
			case "caller":
				spec.ExtraEnv = map[string]string{"CODEX_AUTH_JSON": invalid}
			case "operator":
				f.secrets = map[string]map[string]string{jm.SecretName: {"CODEX_AUTH_JSON": b64(invalid)}}
			}
			err := jm.CreateSessionJob(spec)
			if err == nil || strings.Contains(err.Error(), "fixture-refresh-private") || len(f.created) != 0 || len(f.createdSecrets) != 0 {
				t.Fatal("invalid credential did not fail closed safely")
			}
		})
	}
}
