package gitprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Private asks each provider's own repository endpoint with the caller's
// token in a header, and "can't tell" (a 404, a failure, a missing field) is
// an error — never "public".
func TestPrivate(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.EscapedPath(), r.Header.Get("Authorization")
		switch r.URL.EscapedPath() {
		case "/repos/acme/secret":
			_, _ = w.Write([]byte(`{"full_name":"acme/secret","private":true}`))
		case "/repos/torvalds/linux":
			_, _ = w.Write([]byte(`{"full_name":"torvalds/linux","private":false}`))
		case "/api/v4/projects/grp%2Ftool":
			_, _ = w.Write([]byte(`{"path_with_namespace":"grp/tool","visibility":"internal"}`))
		case "/api/v4/projects/grp%2Fpub":
			_, _ = w.Write([]byte(`{"path_with_namespace":"grp/pub","visibility":"public"}`))
		case "/api/v4/projects/grp%2Fodd":
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	gh := NewGitHubAt("gh", "gh.example", srv.URL, srv.Client())
	gl := NewGitLabAt("gl", "gl.example", srv.URL, srv.Client())
	ctx := context.Background()

	for _, c := range []struct {
		p           Provider
		owner, name string
		private, ok bool
		path        string
	}{
		{gh, "acme", "secret", true, true, "/repos/acme/secret"},
		{gh, "torvalds", "linux", false, true, "/repos/torvalds/linux"},
		{gh, "acme", "gone", false, false, "/repos/acme/gone"},
		{gl, "grp", "tool", true, true, "/api/v4/projects/grp%2Ftool"},
		{gl, "grp", "pub", false, true, "/api/v4/projects/grp%2Fpub"},
		{gl, "grp", "odd", false, false, "/api/v4/projects/grp%2Fodd"},
	} {
		private, err := c.p.Private(ctx, "tok-123", c.owner, c.name)
		if (err == nil) != c.ok || (c.ok && private != c.private) {
			t.Errorf("%s %s/%s: private=%v err=%v, want private=%v ok=%v", c.p.ID(), c.owner, c.name, private, err, c.private, c.ok)
		}
		if gotPath != c.path || gotAuth != "Bearer tok-123" {
			t.Errorf("%s %s/%s: asked %s with %q", c.p.ID(), c.owner, c.name, gotPath, gotAuth)
		}
	}
}
