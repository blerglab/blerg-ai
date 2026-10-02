package db

import (
	"errors"
	"strings"
	"testing"
)

const testSession = "0b9f3c1e-6a41-4c57-9d0e-2f4a7c8b1e55"

func TestValidateLinksArtifactShape(t *testing.T) {
	good := "https://runner.example/sessions/" + testSession + "?artifact=ab12_CD-9"
	label := "spec.pdf (v2)"
	if err := validateLinks([]Link{{Kind: "artifact", URL: good, Label: &label}}); err != nil {
		t.Fatalf("a runner viewer URL must be accepted: %v", err)
	}
	bad := map[string]string{
		"script scheme":     "javascript:alert(1)",
		"data scheme":       "data:text/html,hi",
		"no host":           "https:///sessions/" + testSession + "?artifact=a1",
		"userinfo":          "https://user:pw@runner.example/sessions/" + testSession + "?artifact=a1",
		"not a session id":  "https://runner.example/sessions/not-a-uuid?artifact=a1",
		"no artifact param": "https://runner.example/sessions/" + testSession,
		"extra param":       "https://runner.example/sessions/" + testSession + "?artifact=a1&next=//evil",
		"bad artifact id":   "https://runner.example/sessions/" + testSession + "?artifact=a%20b",
		"fragment":          good + "#x",
		"empty":             "",
	}
	for name, u := range bad {
		err := validateLinks([]Link{{Kind: "artifact", URL: u}})
		if !errors.Is(err, ErrInvalidLink) {
			t.Errorf("%s: want ErrInvalidLink, got %v", name, err)
		}
	}
	long := strings.Repeat("x", maxLinkLabel+1)
	if err := validateLinks([]Link{{Kind: "artifact", URL: good, Label: &long}}); !errors.Is(err, ErrInvalidLink) {
		t.Errorf("an overlong label must be rejected, got %v", err)
	}
}

func TestValidateLinksArtifactForgeries(t *testing.T) {
	for name, u := range map[string]string{
		"two artifact params": "https://runner.example/sessions/" + testSession + "?artifact=a&artifact=b",
		"trailing ampersand":  "https://runner.example/sessions/" + testSession + "?artifact=a&",
		"dot segments":        "https://runner.example/x/../" + testSession + "?artifact=a",
		"double slash path":   "https://runner.example//evil.example/" + testSession + "?artifact=a",
		"encoded slash":       "https://runner.example/sessions%2F" + testSession + "?artifact=a",
		"encoded session":     "https://runner.example/sessions/%30b9f3c1e-6a41-4c57-9d0e-2f4a7c8b1e55?artifact=a",
	} {
		if err := validateLinks([]Link{{Kind: "artifact", URL: u}}); !errors.Is(err, ErrInvalidLink) {
			t.Errorf("%s: want ErrInvalidLink, got %v", name, err)
		}
	}
}

// The kinds a card UI turns into a clickable address must be web addresses; the kinds that are not addresses
// (rcca, doc) are left alone.
func TestValidateLinksWebKindsNeedAWebAddress(t *testing.T) {
	for _, k := range []string{"pr", "url", "session"} {
		for _, bad := range []string{"javascript:alert(1)", "data:text/html,x", "vbscript:x", "file:///etc/passwd", "not a url"} {
			if err := validateLinks([]Link{{Kind: k, URL: bad}}); !errors.Is(err, ErrInvalidLink) {
				t.Errorf("%s %q: want ErrInvalidLink, got %v", k, bad, err)
			}
		}
		if err := validateLinks([]Link{{Kind: k, URL: "https://example.com/a?b=c"}}); err != nil {
			t.Errorf("%s: an https address must be accepted: %v", k, err)
		}
	}
	for _, k := range []string{"rcca", "doc"} {
		if err := validateLinks([]Link{{Kind: k, URL: "anything goes here"}}); err != nil {
			t.Errorf("kind %s must not be validated: %v", k, err)
		}
	}
}

func TestLinksAndAddLinksTogetherAreRefused(t *testing.T) {
	both := CardParams{Links: &[]Link{{Kind: "pr", URL: "https://a/b"}}, AddLinks: &[]Link{{Kind: "pr", URL: "https://a/c"}}}
	if err := ValidateParamLinks(both); !errors.Is(err, ErrInvalidLink) {
		t.Fatalf("links with add_links must be refused, got %v", err)
	}
}

func TestNewLinksSkipsWhatIsAlreadyThere(t *testing.T) {
	have := []Link{{Kind: "pr", URL: "u1"}, {Kind: "artifact", URL: "u2"}}
	add := []Link{{Kind: "pr", URL: "u1"}, {Kind: "artifact", URL: "u3"}, {Kind: "artifact", URL: "u3"}, {Kind: "url", URL: "u1"}}
	got := newLinks(have, add)
	if len(got) != 2 || got[0].URL != "u3" || got[1].Kind != "url" {
		t.Fatalf("got %+v, want one new artifact u3 and the url u1 (same URL, different kind)", got)
	}
}
