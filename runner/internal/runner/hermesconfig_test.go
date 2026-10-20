package runner

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"gopkg.in/yaml.v3"
)

// imageDefault stands in for the config.yaml the Hermes installer leaves in
// the agent image: a hosted default model and unrelated settings.
const imageDefault = `# Hermes defaults
model:
  default: "anthropic/claude-opus-4.6"
  provider: "auto"
  base_url: "https://openrouter.ai/api/v1"
providers:
  personal:
    base_url: https://example.invalid/v1
custom_providers:
  - name: keep-me
    base_url: https://keep.example.invalid/v1
  - name: shared-dup
    base_url: https://old.example.invalid/v1
display:
  compact: true
`

const sharedFragment = `model:
  provider: custom
  base_url: http://[fd12:3456::10]:8000/v1
  default: nemotron-super
providers:
  local-super-vl:
    base_url: http://[fd12:3456::11]:8000/v1
    api_mode: chat_completions
custom_providers:
  - name: shared-dup
    base_url: http://[fd12:3456::12]:8000/v1
`

func decodeYAML(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatalf("merged config is not YAML: %v", err)
	}
	return out
}

func TestMergeHermesConfig(t *testing.T) {
	merged, err := mergeHermesConfig([]byte(imageDefault), []byte(sharedFragment))
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeYAML(t, merged)
	model := doc["model"].(map[string]any)
	if model["provider"] != "custom" || model["base_url"] != "http://[fd12:3456::10]:8000/v1" || model["default"] != "nemotron-super" {
		t.Errorf("model = %v, want the shared default", model)
	}
	if _, ok := model["api_key"]; ok {
		t.Error("model kept a key from the image default")
	}
	providers := doc["providers"].(map[string]any)
	if providers["personal"] == nil || providers["local-super-vl"] == nil {
		t.Errorf("providers = %v, want both the existing and the shared one", providers)
	}
	var names, bases []string
	for _, p := range doc["custom_providers"].([]any) {
		m := p.(map[string]any)
		names = append(names, m["name"].(string))
		bases = append(bases, m["base_url"].(string))
	}
	if strings.Join(names, ",") != "keep-me,shared-dup" || bases[1] != "http://[fd12:3456::12]:8000/v1" {
		t.Errorf("custom_providers = %v %v, want keep-me then the shared shared-dup", names, bases)
	}
	if d := doc["display"].(map[string]any); d["compact"] != true {
		t.Errorf("unrelated image settings lost: %v", doc["display"])
	}
	// A remote provider the base already had keeps the merged file non-local:
	// the desktop rule, applied to everything the session can route to.
	if daemon.HermesConfigLocal(merged) {
		t.Error("merged config with a remote provider claims to be local")
	}
	// Over the installer's defaults alone, the shared default replaces the
	// hosted one, and the file the desktop route parser reads is local.
	installer := "model:\n  default: \"anthropic/claude-opus-4.6\"\n  provider: \"auto\"\n  base_url: \"https://openrouter.ai/api/v1\"\ndelegation:\n  max_iterations: 250\n"
	merged, err = mergeHermesConfig([]byte(installer), []byte(sharedFragment))
	if err != nil {
		t.Fatal(err)
	}
	if !daemon.HermesConfigLocal(merged) {
		t.Errorf("merged installer defaults are not local by the desktop rule:\n%s", merged)
	}
}

func TestMergeHermesConfigWithoutBase(t *testing.T) {
	for name, base := range map[string]string{"absent": "", "unparseable": "model: [\n", "scalar": "just text\n"} {
		merged, err := mergeHermesConfig([]byte(base), []byte(sharedFragment))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		doc := decodeYAML(t, merged)
		if doc["providers"].(map[string]any)["local-super-vl"] == nil {
			t.Errorf("%s: shared providers missing", name)
		}
	}
}

func TestMergeHermesConfigRefusesBadFragment(t *testing.T) {
	for name, frag := range map[string]string{
		"scalar":    "hello\n",
		"sequence":  "- a\n- b\n",
		"alias":     "model: &m\n  provider: custom\nother: *m\n",
		"two docs":  "model: {}\n---\nmodel: {}\n",
		"providers": "providers: [a]\n",
		"broken":    "model: [\n",
	} {
		if _, err := mergeHermesConfig([]byte(imageDefault), []byte(frag)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A fragment over 64 KiB is refused with a clear error (a single environment
// entry over 128 KiB cannot even reach the process); the base config the
// image ships (about 100 KB) is still accepted.
func TestMergeHermesConfigFragmentCap(t *testing.T) {
	big := sharedFragment + "# " + strings.Repeat("x", 64<<10) + "\n"
	_, err := mergeHermesConfig([]byte(imageDefault), []byte(big))
	if err == nil || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("oversized fragment: err = %v, want a 64 KiB error", err)
	}
	base := imageDefault + "# " + strings.Repeat("y", 200<<10) + "\n"
	if _, err := mergeHermesConfig([]byte(base), []byte(sharedFragment)); err != nil {
		t.Fatalf("large image base refused: %v", err)
	}
}

func TestMaterializeHermesConfigWritesPrivateMergedFile(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".hermes", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(imageDefault), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(daemon.HermesProvidersEnvVar, sharedFragment)
	applied, err := materializeHermesConfig(home)
	if err != nil || !applied {
		t.Fatalf("materialize: applied=%v err=%v", applied, err)
	}
	if _, present := os.LookupEnv(daemon.HermesProvidersEnvVar); present {
		t.Error("the fragment is still in the environment the engine inherits")
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("config.yaml mode = %v, want 0600", st.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	doc := decodeYAML(t, data)
	if doc["providers"].(map[string]any)["local-super-vl"] == nil || doc["display"] == nil {
		t.Errorf("not the merge of image default and fragment: %s", data)
	}
	if dir, _ := os.Stat(filepath.Dir(path)); dir.Mode().Perm()&0o077 != 0 && dir.Mode().Perm() != 0o755 {
		t.Errorf("unexpected .hermes mode %v", dir.Mode().Perm())
	}
}

func TestMaterializeHermesConfigCreatesFileWithoutBase(t *testing.T) {
	home := t.TempDir()
	t.Setenv(daemon.HermesProvidersEnvVar, sharedFragment)
	if applied, err := materializeHermesConfig(home); err != nil || !applied {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	st, err := os.Stat(filepath.Join(home, ".hermes", "config.yaml"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("config.yaml: %v %v", st, err)
	}
	if dir, _ := os.Stat(filepath.Join(home, ".hermes")); dir.Mode().Perm() != 0o700 {
		t.Errorf(".hermes mode = %v, want 0700", dir.Mode().Perm())
	}
}

// No shared configuration: nothing is written and an existing file is left
// byte for byte, exactly as before this feature.
func TestMaterializeHermesConfigAbsentLeavesFileAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv(daemon.HermesProvidersEnvVar, "")
	if applied, err := materializeHermesConfig(home); err != nil || applied {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".hermes")); !os.IsNotExist(err) {
		t.Error("created .hermes with no shared configuration")
	}
	path := filepath.Join(home, ".hermes", "config.yaml")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(imageDefault), 0o644)
	if applied, err := materializeHermesConfig(home); err != nil || applied {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	if data, _ := os.ReadFile(path); string(data) != imageDefault {
		t.Error("existing config.yaml changed with no shared configuration")
	}
}

// Neither a success nor a failure puts a value from the fragment into the
// pod log (which `kubectl logs` and the events show).
func TestMaterializeHermesConfigLogsNoValues(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	secret := "sk-synthetic-provider-key-123"
	for _, frag := range []string{
		sharedFragment + "  extra:\n    base_url: http://[fd12:3456::13]:8000/v1\n    api_key: " + secret + "\n",
		"model: [" + secret + "\n",
	} {
		home := t.TempDir()
		t.Setenv(daemon.HermesProvidersEnvVar, frag)
		applied, err := materializeHermesConfig(home)
		logHermesConfigResult(applied, err)
		if err != nil && strings.Contains(err.Error(), secret) {
			t.Error("error carries a fragment value")
		}
	}
	if strings.Contains(buf.String(), secret) || strings.Contains(buf.String(), "fd12:3456") {
		t.Errorf("log carries fragment values: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "hermes") {
		t.Errorf("nothing logged: %q", buf.String())
	}
}
