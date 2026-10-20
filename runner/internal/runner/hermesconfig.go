package runner

import (
	"bytes"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"gopkg.in/yaml.v3"
)

// The cluster's shared Hermes provider configuration (install/k8s,
// HERMES_PROVIDERS_FILE) reaches a Hermes session pod as one secret-backed
// environment variable. The pod merges it into ~/.hermes/config.yaml — the
// file the Hermes CLI and the agent host's model routing read — on top of what
// is already there: the image's installer defaults, or a config.yaml from the
// person's own config bundle.

// hermesConfigMaxBytes bounds the existing config.yaml, as the desktop parser
// does (the installer's own default is about 100 KB).
const hermesConfigMaxBytes = 1 << 20

// hermesFragmentMaxBytes bounds the shared fragment. It arrives as one
// environment entry, and Linux refuses any single entry over 128 KiB; the
// server and deploy.sh enforce the same 64 KiB.
const hermesFragmentMaxBytes = 64 << 10

var errHermesFragmentSize = errors.New("the shared Hermes provider configuration is larger than 64 KiB")

var errHermesFragment = errors.New("the shared Hermes provider configuration is not a usable config.yaml mapping")

// materializeHermesConfig consumes daemon.HermesProvidersEnvVar. Unset or
// empty: nothing is written and applied is false, so a cluster without a
// shared configuration behaves exactly as before. Otherwise the merged
// document replaces $home/.hermes/config.yaml with mode 0600. The variable is
// removed from the environment before anything else happens, so the engine
// never inherits it. Errors never carry a value from either document.
func materializeHermesConfig(home string) (applied bool, err error) {
	raw, present := os.LookupEnv(daemon.HermesProvidersEnvVar)
	if present {
		_ = os.Unsetenv(daemon.HermesProvidersEnvVar)
	}
	if len(bytes.TrimSpace([]byte(raw))) == 0 {
		return false, nil
	}
	dir := filepath.Join(home, ".hermes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, errors.New("the Hermes configuration directory could not be created")
	}
	path := filepath.Join(dir, "config.yaml")
	base, err := readExistingHermesConfig(path)
	if err != nil {
		return false, err
	}
	merged, err := mergeHermesConfig(base, []byte(raw))
	if err != nil {
		return false, err
	}
	if err := writePrivateFile(path, merged); err != nil {
		return false, errors.New("the Hermes configuration could not be written")
	}
	return true, nil
}

// logHermesConfigResult reports materializeHermesConfig's outcome without any
// value from the configuration.
func logHermesConfigResult(applied bool, err error) {
	switch {
	case err != nil:
		log.Printf("runner: hermes provider config: %v (continuing with the existing Hermes configuration)", err)
	case applied:
		log.Printf("runner: hermes provider config: the cluster's shared providers were merged into ~/.hermes/config.yaml")
	}
}

// readExistingHermesConfig returns the current config.yaml, or nothing when
// there is none. Anything but a regular file is refused rather than followed.
func readExistingHermesConfig(path string) ([]byte, error) {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !st.Mode().IsRegular() {
		return nil, errors.New("the existing ~/.hermes/config.yaml is not a regular file")
	}
	f, err := os.Open(path) //nolint:gosec // fixed name under the pod's own home, checked regular above
	if err != nil {
		return nil, errors.New("the existing ~/.hermes/config.yaml could not be read")
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, hermesConfigMaxBytes+1))
	if err != nil || len(data) > hermesConfigMaxBytes {
		return nil, errors.New("the existing ~/.hermes/config.yaml could not be read")
	}
	return data, nil
}

// writePrivateFile replaces path atomically with a 0600 file.
func writePrivateFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// mergeHermesConfig lays fragment over base, both Hermes config.yaml
// documents:
//
//   - `providers` (a mapping of named providers) is merged by provider name;
//   - `custom_providers` (a list of {name: ...}) is merged by name, a shared
//     entry replacing a same-named one in place and the rest appended;
//   - every other top-level key of fragment, `model` included, replaces the
//     base's value whole, so no stale key of a replaced block survives.
//
// Keys base has and fragment does not are kept. An empty, unparseable or
// non-mapping base is ignored (the fragment alone is written). The fragment
// must be one mapping document without anchors or aliases.
func mergeHermesConfig(base, fragment []byte) ([]byte, error) {
	if len(fragment) > hermesFragmentMaxBytes {
		return nil, errHermesFragmentSize
	}
	frag, ok := yamlMappingDocument(fragment)
	if !ok {
		return nil, errHermesFragment
	}
	for i := 0; i < len(frag.Content); i += 2 {
		v := frag.Content[i+1]
		switch frag.Content[i].Value {
		case "providers":
			if v.Kind != yaml.MappingNode {
				return nil, errHermesFragment
			}
		case "custom_providers":
			if v.Kind != yaml.SequenceNode {
				return nil, errHermesFragment
			}
		}
	}
	out, ok := yamlMappingDocument(base)
	if !ok {
		out = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	for i := 0; i < len(frag.Content); i += 2 {
		key, value := frag.Content[i], frag.Content[i+1]
		existing := mappingValue(out, key.Value)
		switch {
		case existing == nil:
			out.Content = append(out.Content, key, value)
		case key.Value == "providers" && existing.Kind == yaml.MappingNode:
			for j := 0; j < len(value.Content); j += 2 {
				setMappingValue(existing, value.Content[j], value.Content[j+1])
			}
		case key.Value == "custom_providers" && existing.Kind == yaml.SequenceNode:
			for _, entry := range value.Content {
				name := customProviderName(entry)
				replaced := false
				for k, old := range existing.Content {
					if name != "" && customProviderName(old) == name {
						existing.Content[k] = entry
						replaced = true
						break
					}
				}
				if !replaced {
					existing.Content = append(existing.Content, entry)
				}
			}
		default:
			setMappingValue(out, key, value)
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(out); err != nil {
		return nil, errHermesFragment
	}
	if err := enc.Close(); err != nil {
		return nil, errHermesFragment
	}
	return buf.Bytes(), nil
}

// yamlMappingDocument decodes exactly one YAML document whose root is a
// mapping with plain string keys, refusing anchors, aliases and merge keys
// anywhere (the desktop route parser refuses them too).
func yamlMappingDocument(data []byte) (*yaml.Node, bool) {
	if len(bytes.TrimSpace(data)) == 0 || len(data) > hermesConfigMaxBytes {
		return nil, false
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if dec.Decode(&doc) != nil {
		return nil, false
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, false
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, false
	}
	var plain func(*yaml.Node, int) bool
	plain = func(n *yaml.Node, depth int) bool {
		if depth > 32 || n.Kind == yaml.AliasNode || n.Anchor != "" {
			return false
		}
		if n.Kind == yaml.MappingNode {
			for i := 0; i < len(n.Content); i += 2 {
				if k := n.Content[i]; k.Kind != yaml.ScalarNode || k.Value == "<<" {
					return false
				}
			}
		}
		for _, c := range n.Content {
			if !plain(c, depth+1) {
				return false
			}
		}
		return true
	}
	if !plain(doc.Content[0], 0) {
		return nil, false
	}
	return doc.Content[0], true
}

func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func setMappingValue(m, key, value *yaml.Node) {
	for i := 0; i < len(m.Content); i += 2 {
		if m.Content[i].Value == key.Value {
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content, key, value)
}

func customProviderName(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.MappingNode {
		return ""
	}
	if v := mappingValue(n, "name"); v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}
