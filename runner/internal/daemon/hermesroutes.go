package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/models"
	"gopkg.in/yaml.v3"
)

const hermesMaxProviders = 16

var hermesProviderName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)
var errHermesRoute = errors.New("hermes model route is unavailable or unsafe; check the configured named provider and its model catalog")

type hermesProviderRoute struct {
	Name, BaseURL, Default string
	digest                 string
	policyDigest           string
	legacy                 bool
	Credential, Disabled   bool
}
type hermesRouteConfig struct {
	Default           hermesModelConfig
	Providers         []hermesProviderRoute
	Fallback, Unknown bool
	LocalUnknown      bool
	defaultDigest     string
	defaultPolicy     string
}
type hermesModelRoute struct {
	Selector, Model, Provider string
	baseURL                   string
	fingerprint               string
}

// Parse bounded operator YAML without retaining secret fields in the routing
// result. Aliases, merges and duplicate keys are refused throughout the document;
// the adapter never guesses a native route from ambiguous configuration.
func readHermesRouteConfig(path string) (hermesRouteConfig, error) {
	var out hermesRouteConfig
	if _, err := readHermesModelConfig(path); err != nil {
		return out, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // trusted operator config, regular-file check and bounded read below.
	if err != nil {
		return out, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > hermesConfigMaxRead {
		return out, errHermesRoute
	}
	data, err := io.ReadAll(io.LimitReader(f, hermesConfigMaxRead+1))
	if err != nil || len(data) > hermesConfigMaxRead {
		return out, errHermesRoute
	}
	return parseHermesRouteConfig(data)
}

// parseHermesRouteConfig is readHermesRouteConfig over bytes already in
// memory. It applies the same line-parser gate first, so a document either
// reader refuses is refused here too.
func parseHermesRouteConfig(data []byte) (hermesRouteConfig, error) {
	var out hermesRouteConfig
	if len(data) > hermesConfigMaxRead {
		return out, errHermesRoute
	}
	if _, err := scanHermesModelConfig(bytes.NewReader(data)); err != nil {
		return out, err
	}
	var doc yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&doc) != nil {
		return out, errHermesRoute
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return out, errHermesRoute
	}
	nodes := 0
	var valid func(*yaml.Node, int) bool
	valid = func(n *yaml.Node, depth int) bool {
		nodes++
		if nodes > 16384 || depth > 24 || n.Kind == yaml.AliasNode {
			return false
		}
		if n.Kind == yaml.MappingNode {
			seen := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				k := n.Content[i]
				if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || k.Value == "<<" || seen[k.Value] {
					return false
				}
				seen[k.Value] = true
			}
		}
		for _, child := range n.Content {
			if !valid(child, depth+1) {
				return false
			}
		}
		return true
	}
	if len(doc.Content) != 1 || !valid(&doc, 0) || doc.Content[0].Kind != yaml.MappingNode {
		return out, errHermesRoute
	}
	mapping := func(n *yaml.Node) map[string]*yaml.Node {
		m := map[string]*yaml.Node{}
		if n != nil && n.Kind == yaml.MappingNode {
			for i := 0; i < len(n.Content); i += 2 {
				m[n.Content[i].Value] = n.Content[i+1]
			}
		}
		return m
	}
	scalar := func(n *yaml.Node) string {
		if n != nil && n.Kind == yaml.ScalarNode && n.Tag == "!!str" {
			return strings.TrimSpace(n.Value)
		}
		return ""
	}
	root := mapping(doc.Content[0])
	model := mapping(root["model"])
	out.defaultDigest = hermesNodeDigest(root["model"])
	out.defaultPolicy = hermesPolicyDigest(model, model)
	out.Default = hermesModelConfig{Provider: scalar(model["provider"]), BaseURL: scalar(model["base_url"]), Default: scalar(model["default"])}
	credential := func(m map[string]*yaml.Node) bool {
		for _, k := range []string{"api_key", "apiKey", "key", "key_env", "api_key_env", "key_cmd", "keyEnv", "keyCmd", "apiKeyEnv", "extra_headers", "headers"} {
			if m[k] != nil {
				return true
			}
		}
		return false
	}
	out.Unknown = credential(model) || root["model_aliases"] != nil || model["aliases"] != nil
	for _, key := range []string{"delegation", "auxiliary"} {
		if hermesAuxiliaryRouting(root[key]) {
			out.LocalUnknown = true
		}
	}
	for _, k := range []string{"fallback_model", "fallback_providers"} {
		n := root[k]
		if n != nil && n.Tag != "!!null" && ((n.Kind != yaml.SequenceNode && n.Kind != yaml.MappingNode) || len(n.Content) != 0) {
			out.Fallback = true
		}
	}
	providers := root["providers"]
	if providers != nil && providers.Kind != yaml.MappingNode {
		return out, errHermesRoute
	}
	type candidate struct {
		name   string
		node   *yaml.Node
		legacy bool
	}
	var candidates []candidate
	if legacy := root["custom_providers"]; legacy != nil && legacy.Tag != "!!null" {
		if legacy.Kind != yaml.SequenceNode {
			return out, errHermesRoute
		}
		for _, node := range legacy.Content {
			m := mapping(node)
			name := strings.TrimPrefix(strings.ReplaceAll(strings.ToLower(scalar(m["name"])), " ", "-"), "custom:")
			candidates = append(candidates, candidate{name, node, true})
		}
	}
	if providers != nil {
		for i := 0; i < len(providers.Content); i += 2 {
			candidates = append(candidates, candidate{providers.Content[i].Value, providers.Content[i+1], false})
		}
	}
	for _, candidate := range candidates {
		name, n := candidate.name, candidate.node
		for _, existing := range out.Providers {
			if existing.Name == name {
				return out, errHermesRoute
			}
		}
		if !hermesProviderName.MatchString(name) || len(out.Providers) >= hermesMaxProviders || n.Kind != yaml.MappingNode {
			return out, errHermesRoute
		}
		m := mapping(n)
		if m["disabled"] != nil || (candidate.legacy && m["enabled"] != nil) {
			return out, errHermesRoute
		}
		if nameOverride := scalar(m["name"]); !candidate.legacy && nameOverride != "" && nameOverride != name {
			return out, errHermesRoute
		}
		p := hermesProviderRoute{Name: name, Credential: credential(m), Default: scalar(m["model"]), digest: hermesNodeDigest(n), policyDigest: hermesPolicyDigest(m, model), legacy: candidate.legacy}
		if p.Default == "" {
			p.Default = scalar(m["default_model"])
		}
		for _, k := range []string{"base_url", "baseUrl", "api", "url"} {
			if m[k] != nil {
				v := scalar(m[k])
				if v == "" || (p.BaseURL != "" && p.BaseURL != v) {
					return out, errHermesRoute
				}
				if !validHermesRouteURL(v) {
					return out, errHermesRoute
				}
				p.BaseURL = v
			}
		}
		for _, k := range []string{"enabled"} {
			if n := m[k]; n != nil {
				if n.Tag != "!!bool" {
					return out, errHermesRoute
				}
				if strings.EqualFold(n.Value, "false") {
					p.Disabled = true
				}
			}
		}
		for _, k := range []string{"api_mode", "apiMode", "transport"} {
			if m[k] != nil {
				mode := scalar(m[k])
				if mode != "chat_completions" && mode != "openai" && mode != "openai_chat" {
					return out, errHermesRoute
				}
			}
		}
		out.Providers = append(out.Providers, p)
	}
	return out, nil
}

func (d *hermesDriver) modelConfigPath() string {
	home, profile := "", ""
	for _, entry := range d.env {
		if v, ok := strings.CutPrefix(entry, "HOME="); ok {
			home = v
		}
		if v, ok := strings.CutPrefix(entry, "HERMES_HOME="); ok {
			profile = v
		}
	}
	if d.prefix.enabled() {
		return filepath.Join(homeDir(), ".hermes", "config.yaml")
	}
	if profile != "" {
		return filepath.Join(profile, "config.yaml")
	}
	if home == "" {
		home = homeDir()
	}
	return filepath.Join(home, ".hermes", "config.yaml")
}
func (d *hermesDriver) resolveModel(ctx context.Context, selector string) (hermesModelRoute, error) {
	route := hermesModelRoute{Selector: selector, Model: selector}
	cfg, err := readHermesRouteConfig(d.modelConfigPath())
	qualified := strings.HasPrefix(selector, "custom:")
	if !qualified && (err != nil || (cfg.Default.Provider != "custom" && cfg.Default.Provider != "") || cfg.Default.BaseURL == "") {
		return route, nil //nolint:nilerr // Preserve legacy raw model selection; named routes below always require readable config.
	}
	if !qualified && err == nil && (cfg.Fallback || cfg.Unknown) {
		return route, nil
	}
	if err != nil || cfg.Fallback || cfg.Unknown {
		return route, errHermesRoute
	}
	provider, model := "custom", selector
	if qualified {
		var ok bool
		provider, model, ok = strings.Cut(selector, "/")
		if !ok || !models.ValidModelFor("hermes", selector) || !validHermesNativeModel(model) {
			return route, errHermesRoute
		}
	} else {
		if model == "" {
			model = cfg.Default.Default
			route.Model = model
			route.Selector = model
		}
		if model == "" {
			return route, nil
		}
		if !validHermesNativeModel(model) || !validHermesRouteURL(cfg.Default.BaseURL) {
			return route, errHermesRoute
		}
		for _, p := range cfg.Providers {
			if !p.Disabled && p.BaseURL == cfg.Default.BaseURL && !p.Credential && p.policyDigest == cfg.defaultPolicy {
				provider = "custom:" + p.Name
				break
			}
		}
		if provider == "custom" {
			if len(cfg.Providers) > 0 {
				return route, errHermesRoute
			}
			return hermesModelRoute{Selector: route.Selector, Model: model, Provider: provider, baseURL: cfg.Default.BaseURL, fingerprint: hermesRouteFingerprint(provider, model, cfg.Default.BaseURL, cfg.defaultDigest)}, nil
		}
	}
	for _, p := range cfg.Providers {
		if "custom:"+p.Name != provider {
			continue
		}
		if p.Disabled || p.Credential || p.BaseURL == "" {
			return route, errHermesRoute
		}
		list, err := fetchOpenAIModels(ctx, newHermesProbeClient(), p.BaseURL, p.Default)
		if err != nil {
			return route, errHermesRoute
		}
		for _, m := range list {
			if m.ID == model {
				return hermesModelRoute{Selector: route.Selector, Model: model, Provider: provider, baseURL: p.BaseURL, fingerprint: hermesRouteFingerprint(provider, model, p.BaseURL, p.digest+"."+p.policyDigest)}, nil
			}
		}
	}
	return route, errHermesRoute
}

// Do not send input until a fully built native session confirms both route
// components. Lazy draft info is only an echoed request, not proof of routing.
func (r *hermesRun) confirmRoute() bool {
	if r.binding.Provider == "" {
		return true
	}
	if !r.validRouteConfig() {
		return false
	}
	timer := time.NewTimer(120 * time.Second)
	defer timer.Stop()
	for {
		for {
			frame, ok := r.rpc.pop()
			if !ok {
				break
			}
			r.handle(frame)
		}
		if r.routeConfirmed {
			return r.validRouteConfig()
		}
		select {
		case <-r.rpc.wake:
		case <-r.rpc.done:
			return false
		case <-r.ctx.Done():
			return false
		case <-timer.C:
			return false
		}
	}
}

func validHermesRouteURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && !strings.ContainsAny(raw, "{}$\\\r\n\t")
}

func (r *hermesRun) validRouteConfig() bool {
	cfg, err := readHermesRouteConfig(r.d.modelConfigPath())
	if err != nil || cfg.Fallback || cfg.Unknown {
		return false
	}
	found := r.binding.Provider == "custom" && len(cfg.Providers) == 0 && (cfg.Default.Provider == "custom" || cfg.Default.Provider == "") && cfg.Default.BaseURL == r.binding.baseURL && r.binding.fingerprint == hermesRouteFingerprint("custom", r.binding.Model, cfg.Default.BaseURL, cfg.defaultDigest)
	for _, p := range cfg.Providers {
		if "custom:"+p.Name == r.binding.Provider && !p.Disabled && !p.Credential && p.BaseURL == r.binding.baseURL && r.binding.fingerprint == hermesRouteFingerprint(r.binding.Provider, r.binding.Model, p.BaseURL, p.digest+"."+p.policyDigest) {
			found = true
		}
	}
	return found
}

func hermesNodeDigest(n *yaml.Node) string {
	if n == nil {
		return ""
	}
	var value map[string]any
	if n.Decode(&value) != nil {
		return ""
	}
	// Native discovery may update these without changing the selected runtime.
	for _, key := range []string{"models", "models_discovered", "name", "model", "default_model", "defaultModel", "default", "provider", "discover_models"} {
		delete(value, key)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func hermesRouteFingerprint(provider, model, base, digest string) string {
	raw, _ := json.Marshal([]string{"blerg-hermes-route-v1", provider, model, base, digest})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func hermesPolicyDigest(provider, defaults map[string]*yaml.Node) string {
	policy := map[string]any{"api_mode": "chat_completions"}
	for _, pair := range [][2]string{{"api_mode", "api_mode"}, {"apiMode", "api_mode"}, {"transport", "api_mode"}, {"context_length", "context_length"}, {"contextLength", "context_length"}, {"extra_body", "extra_body"}, {"ssl_verify", "ssl_verify"}, {"ssl_ca_cert", "ssl_ca_cert"}, {"request_timeout_seconds", "request_timeout_seconds"}, {"stale_timeout_seconds", "stale_timeout_seconds"}, {"rate_limit_delay", "rate_limit_delay"}} {
		n := provider[pair[0]]
		if n == nil {
			n = defaults[pair[0]]
		}
		if n != nil {
			var value any
			if n.Decode(&value) == nil {
				mode, _ := value.(string)
				if pair[1] == "api_mode" && (mode == "openai" || mode == "openai_chat") {
					value = "chat_completions"
				}
				policy[pair[1]] = value
			}
		}
	}
	raw, _ := json.Marshal(policy)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Only routing overrides affect this conservative hardware-budget claim;
// empty native auto/default blocks and ordinary reasoning limits do not.
func hermesAuxiliaryRouting(n *yaml.Node) bool {
	if n == nil {
		return false
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i < len(n.Content); i += 2 {
			key, value := n.Content[i].Value, n.Content[i+1]
			switch key {
			case "provider", "model", "base_url", "api_key", "api_mode", "key_env", "api_key_env", "key_cmd", "fallback", "fallback_model", "fallback_providers", "openrouter_model", "extra_body", "extra_headers":
				if value.Tag == "!!null" || ((value.Kind == yaml.MappingNode || value.Kind == yaml.SequenceNode) && len(value.Content) == 0) {
					continue
				}
				if value.Kind == yaml.ScalarNode && (value.Value == "" || (key == "provider" && value.Value == "auto")) {
					continue
				}
				return true
			}
			if hermesAuxiliaryRouting(value) {
				return true
			}
		}
	} else if n.Kind == yaml.SequenceNode {
		for _, child := range n.Content {
			if hermesAuxiliaryRouting(child) {
				return true
			}
		}
	}
	return false
}

func validHermesNativeModel(model string) bool {
	return model != "" && models.ValidModelFor("hermes", model) && !strings.Contains(model, "://") && !strings.HasPrefix(model, "custom:")
}
