package daemon

import (
	"context"
	"net"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEndpointIsLocal(t *testing.T) {
	old := lookupIPs
	t.Cleanup(func() { lookupIPs = old })
	lookupIPs = func(_ context.Context, host string) ([]net.IP, error) {
		switch host {
		case "mixed.test":
			return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("93.184.216.34")}, nil
		case "lan.test":
			return []net.IP{net.ParseIP("10.1.2.3")}, nil // scrub:allow
		}
		return nil, &net.DNSError{Err: "no such host", Name: host}
	}
	for url, want := range map[string]bool{
		"http://localhost:11434/v1": true,
		"http://127.0.0.1:11434/v1": true,
		"http://[::1]:8000":         true,
		"http://10.0.0.5:8000/v1":   true, // scrub:allow
		"http://8.8.8.8/v1":         false,
		"https://api.example.com":   false, // does not resolve
		"http://lan.test/v1":        true,
		"http://mixed.test/v1":      false, // every answer must be local
		"":                          false,
		"://bad":                    false,
		"not a url":                 false,
	} {
		if got := endpointIsLocal(url); got != want {
			t.Errorf("endpointIsLocal(%q) = %v, want %v", url, got, want)
		}
	}
}

func TestHermesLocalDetection(t *testing.T) {
	for name, c := range map[string]struct {
		cfg, env string
		want     bool
	}{
		"loopback, no key":             {"model:\n  base_url: http://127.0.0.1:11434/v1\n", "", true},
		"localhost, no key":            {"model:\n  base_url: http://localhost:11434/v1\n", "", true},
		"loopback with a provider key": {"model:\n  base_url: http://127.0.0.1:11434/v1\n", "OPENROUTER_API_KEY=x\n", false},
		"loopback, empty key line":     {"model:\n  base_url: http://127.0.0.1:11434/v1\n", "OPENROUTER_API_KEY=\n", true},
		"public host":                  {"model:\n  base_url: https://api.example.com/v1\n", "", false},
		"no base_url":                  {"model:\n  default: x\n", "", false},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			mustWrite(t, filepath.Join(home, ".hermes", "config.yaml"), c.cfg)
			if c.env != "" {
				mustWrite(t, filepath.Join(home, ".hermes", ".env"), c.env)
			}
			if got := hermesLocal(); got != c.want {
				t.Errorf("hermesLocal() = %v, want %v", got, c.want)
			}
		})
	}
	for _, k := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENROUTER_API_KEY"} {
		t.Run("daemon env key "+k, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			mustWrite(t, filepath.Join(home, ".hermes", "config.yaml"), "model:\n  base_url: http://127.0.0.1:11434/v1\n")
			if !hermesLocal() {
				t.Fatal("precondition: local without a key")
			}
			t.Setenv(k, "x")
			if hermesLocal() {
				t.Error("hermesLocal() = true with a provider key in the daemon environment")
			}
		})
	}
	t.Run("no config", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		if hermesLocal() {
			t.Error("hermesLocal() = true with no config")
		}
	})
}

func TestOpenclawLocalDetection(t *testing.T) {
	for _, k := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENROUTER_API_KEY"} {
		t.Setenv(k, "")
	}
	write := func(t *testing.T, body string) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		if body != "" {
			mustWrite(t, filepath.Join(home, ".openclaw", "openclaw.json"), body)
		}
	}
	t.Run("file absent", func(t *testing.T) {
		write(t, "")
		if openclawLocal() {
			t.Error("true with no file")
		}
	})
	t.Run("loopback baseUrl", func(t *testing.T) {
		write(t, `{"models":{"providers":{"vllm":{"baseUrl":"http://127.0.0.1:8000/v1"}}}}`)
		if !openclawLocal() {
			t.Error("false for a loopback baseUrl")
		}
	})
	t.Run("snake case key, nested in an array", func(t *testing.T) {
		write(t, `{"a":[{"base_url":"http://localhost:1234"}]}`)
		if !openclawLocal() {
			t.Error("false for base_url in an array")
		}
	})
	t.Run("one remote among local", func(t *testing.T) {
		write(t, `{"a":{"baseUrl":"http://127.0.0.1:8000"},"b":{"baseUrl":"https://api.example.com/v1"}}`)
		if openclawLocal() {
			t.Error("true with a remote endpoint present")
		}
	})
	t.Run("no baseUrl at all", func(t *testing.T) {
		write(t, `{"x":1}`)
		if openclawLocal() {
			t.Error("true with no endpoint")
		}
	})
	t.Run("unparseable", func(t *testing.T) {
		write(t, `{not json`)
		if openclawLocal() {
			t.Error("true for an unparseable file")
		}
	})
	t.Run("comments and trailing commas", func(t *testing.T) {
		write(t, "// openclaw\n{\n  /* models */ \"models\": {\"p\": {\"baseUrl\": \"http://127.0.0.1:8000\", // local\n  },},\n  \"note\": \"http://x // not a comment, } ,\",\n}\n")
		if !openclawLocal() {
			t.Error("false for a JSON5-ish config")
		}
	})
	t.Run("comment hiding a remote url is still read", func(t *testing.T) {
		write(t, "{\"a\":{\"baseUrl\":\"https://api.example.com\" /* x */,}}")
		if openclawLocal() {
			t.Error("true with a remote endpoint")
		}
	})
	t.Run("unquoted keys stay fail closed", func(t *testing.T) {
		write(t, `{a:{baseUrl:"http://127.0.0.1:8000"}}`)
		if openclawLocal() {
			t.Error("true for an unparseable file")
		}
	})
	for _, k := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENROUTER_API_KEY"} {
		t.Run("key in env "+k, func(t *testing.T) {
			write(t, `{"a":{"baseUrl":"http://127.0.0.1:8000"}}`)
			t.Setenv(k, "x")
			if openclawLocal() {
				t.Error("true with a provider key in the environment")
			}
		})
	}
}

func TestLocalEnginesListsOnlyLocal(t *testing.T) {
	for _, k := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENROUTER_API_KEY"} {
		t.Setenv(k, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	resetLocalEnginesCache()
	if got := localEngines(); len(got) != 0 {
		t.Fatalf("localEngines() = %v with nothing configured", got)
	}
	mustWrite(t, filepath.Join(home, ".hermes", "config.yaml"), "model:\n  base_url: http://localhost:11434/v1\n")
	mustWrite(t, filepath.Join(home, ".openclaw", "openclaw.json"), `{"p":{"baseUrl":"https://api.example.com"}}`)
	resetLocalEnginesCache()
	if got, want := localEngines(), []string{"hermes"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("localEngines() = %v, want %v", got, want)
	}
	// Cached: a change inside the window is not seen until the cache is reset.
	mustWrite(t, filepath.Join(home, ".openclaw", "openclaw.json"), `{"p":{"baseUrl":"http://127.0.0.1:1"}}`)
	if got := localEngines(); !reflect.DeepEqual(got, []string{"hermes"}) {
		t.Fatalf("cached localEngines() = %v", got)
	}
	resetLocalEnginesCache()
	if got, want := localEngines(), []string{"hermes", "openclaw"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("localEngines() = %v, want %v (engineOrder)", got, want)
	}
}
