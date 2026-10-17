package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
)

func prefsFx(t *testing.T) *mcpFx {
	t.Helper()
	fx := newMCPFx(t)
	return fx
}

func callPrefs(fx *mcpFx, method, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/me/preferences", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	if method == http.MethodGet {
		fx.api.HandleGetMyPreferences(w, req)
	} else {
		fx.api.HandlePutMyPreferences(w, req)
	}
	return w
}

const goodColours = `{"code_colours":{"dark":"nord","light":"github-light","overrides":{"dark":{"keyword":"#AABBCC"}}}}`

func TestPreferencesRoundTripAndPerAccount(t *testing.T) {
	fx := prefsFx(t)
	human := fx.human()
	if w := callPrefs(fx, "GET", human, ""); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "{}" {
		t.Fatalf("fresh GET: %d %s", w.Code, w.Body.String())
	}
	w := callPrefs(fx, "PUT", human, goodColours)
	if w.Code != 200 {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	w = callPrefs(fx, "GET", human, "")
	want := `{"code_colours":{"dark":"nord","light":"github-light","overrides":{"dark":{"keyword":"#aabbcc"},"light":{}}}}`
	if got := strings.TrimSpace(w.Body.String()); got != want {
		t.Fatalf("GET after PUT:\n got %s\nwant %s", got, want)
	}
	// Another account sees nothing of it.
	other := mintRunnerToken(t, fx.priv, "core-1", identity.Claims{
		Sub: "someone-else", Aud: coreAuthAudience, Kind: "human", Sid: testSID,
		Caps: []string{"session.start"}, ExpiresAt: time.Now().Add(time.Minute).Unix(),
	})
	if w := callPrefs(fx, "GET", other, ""); strings.TrimSpace(w.Body.String()) != "{}" {
		t.Fatalf("other account sees %s", w.Body.String())
	}
}

func TestPreferencesRefuseBadInput(t *testing.T) {
	fx := prefsFx(t)
	human := fx.human()
	bad := map[string]string{
		"unknown theme": `{"code_colours":{"dark":"neon","light":"blerg"}}`,
		"bad colour":    `{"code_colours":{"dark":"blerg","light":"blerg","overrides":{"dark":{"keyword":"red"}}}}`,
		"css injection": `{"code_colours":{"dark":"blerg","light":"blerg","overrides":{"dark":{"keyword":"#fff;x:y"}}}}`,
		"unknown token": `{"code_colours":{"dark":"blerg","light":"blerg","overrides":{"dark":{"sparkle":"#ffffff"}}}}`,
		"unknown mode":  `{"code_colours":{"dark":"blerg","light":"blerg","overrides":{"sepia":{}}}}`,
		"unknown field": `{"code_colours":{"dark":"blerg","light":"blerg","extra":1}}`,
		"unknown pref":  `{"theme":"x"}`,
		"not json":      `nope`,
		"empty":         `{}`,
	}
	for name, body := range bad {
		if w := callPrefs(fx, "PUT", human, body); w.Code != 400 {
			t.Errorf("%s: got %d %s, want 400", name, w.Code, w.Body.String())
		}
	}
	big := `{"code_colours":{"dark":"blerg","light":"blerg","pad":"` + strings.Repeat("a", 17<<10) + `"}}`
	if w := callPrefs(fx, "PUT", human, big); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize: got %d", w.Code)
	}
}

func TestPreferencesAreForThePersonOnly(t *testing.T) {
	fx := prefsFx(t)
	for _, m := range []string{"GET", "PUT"} {
		if w := callPrefs(fx, m, "", goodColours); w.Code != 401 {
			t.Errorf("%s anonymous: %d", m, w.Code)
		}
		if w := callPrefs(fx, m, fx.token("agent", ""), goodColours); w.Code != 403 {
			t.Errorf("%s agent token: %d", m, w.Code)
		}
		if w := callPrefs(fx, m, "daemon-tok-1234567890", goodColours); w.Code != 401 {
			t.Errorf("%s daemon token: %d", m, w.Code)
		}
	}
}

// The ids and token keys validated here are the ones the chat package defines.
func TestPreferencesVocabularyMatchesChatPackage(t *testing.T) {
	src, err := os.ReadFile("../../../packages/chat/src/theme/codeThemes.ts")
	if err != nil {
		t.Skip("chat package not alongside")
	}
	text := string(src)
	var ids []string
	for _, m := range regexp.MustCompile(`id: '([a-z-]+)'`).FindAllStringSubmatch(text, -1) {
		ids = append(ids, m[1])
	}
	_, block, ok := strings.Cut(text, "CODE_TOKEN_NAMES = {")
	if !ok {
		t.Fatal("CODE_TOKEN_NAMES not found in codeThemes.ts")
	}
	block, _, _ = strings.Cut(block, "} as const")
	var keys []string
	for _, m := range regexp.MustCompile(`(?m)^\s+(\w+): '--chat-`).FindAllStringSubmatch(block, -1) {
		keys = append(keys, m[1])
	}
	keysOf := func(m map[string]bool) []string {
		var o []string
		for k := range m {
			o = append(o, k)
		}
		sort.Strings(o)
		return o
	}
	sort.Strings(ids)
	sort.Strings(keys)
	if strings.Join(ids, ",") != strings.Join(keysOf(codeThemeIDs), ",") {
		t.Errorf("theme ids drifted: chat %v, runner %v", ids, keysOf(codeThemeIDs))
	}
	if strings.Join(keys, ",") != strings.Join(keysOf(codeTokenKeys), ",") {
		t.Errorf("token keys drifted: chat %v, runner %v", keys, keysOf(codeTokenKeys))
	}
}

// The read an app's chat proxy makes for the person signed in to the app.
func TestRunnerGetPreferencesForAnApp(t *testing.T) {
	fx := prefsFx(t)
	if w := callPrefs(fx, "PUT", fx.human(), goodColours); w.Code != 200 {
		t.Fatalf("PUT: %d", w.Code)
	}
	get := func(token, query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/runner/preferences"+query, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		fx.api.HandleRunnerGetPreferences(w, req)
		return w
	}
	if w := get(runnerTestKey, "?account="+mcpAcct); w.Code != 200 || !strings.Contains(w.Body.String(), `"dark":"nord"`) {
		t.Fatalf("runner key, named account: %d %s", w.Code, w.Body.String())
	}
	if w := get(runnerTestKey, "?account=someone-else"); strings.TrimSpace(w.Body.String()) != "{}" {
		t.Fatalf("another account: %s", w.Body.String())
	}
	if w := get(runnerTestKey, ""); strings.TrimSpace(w.Body.String()) != "{}" {
		t.Fatalf("no account named: %s", w.Body.String())
	}
	if w := get("", "?account="+mcpAcct); w.Code != 401 {
		t.Fatalf("anonymous: %d", w.Code)
	}
	agent := mintRunnerToken(t, fx.priv, "core-1", identity.Claims{
		Sub: "tok-1", Aud: coreAuthAudience, Kind: "agent", OnBehalfOf: mcpAcct,
		Caps: []string{coreAuthRunnerCap}, ExpiresAt: time.Now().Add(time.Minute).Unix(),
	})
	if w := get(agent, "?account="+mcpAcct); w.Code != 403 {
		t.Fatalf("agent token: %d %s", w.Code, w.Body.String())
	}
}
