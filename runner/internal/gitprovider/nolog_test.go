package gitprovider

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// logCallRe matches a line that writes a log line or builds an error/format
// string — the places a secret would leak from.
var logCallRe = regexp.MustCompile(`\b(log\.(Print|Fatal|Panic)\w*|slog\.\w+|fmt\.(Errorf|Sprintf|Printf|Fprintf))\(`)

// tokenURLRe matches an expression that yields a clone URL WITH a token in
// it: authURL(…) (the pod's injector), a provider CloneURL(…) whose token
// argument is not the literal "", the raw token fields/keys, and the pod's
// authenticated clone URL variable; and a daemon's named clone: the token a
// spawn carries (SpawnSession.GitToken, CloneTarget.Token, the server's
// fetched token variable) and the header environment built from it.
var tokenURLRe = regexp.MustCompile(`authURL\(|CloneURL\([^)]*,[^)]*,\s*[^")\s][^)]*\)|\bGitToken\b|gitTokenKey\]|\bcloneURL\b|\.Token\b|tokenHeaderEnv\(|\bgitToken\b`)

// TestTokenBearingURLsAreNeverLogged scans the runner's non-test sources for
// a logging or formatting call that mentions a token-bearing clone URL on the
// same line. It is a tripwire, not a proof: it catches the direct mistake
// (logging the authenticated URL or the token) that review is most likely to
// miss. The server-side clone URL variable of the same name is token-free
// (k8sjobs.go builds it without one), so only the pod's package is checked
// for cloneURL.
func TestTokenBearingURLsAreNeverLogged(t *testing.T) {
	roots := []string{"..", filepath.Join("..", "..", "cmd")}
	scannedLogLines := 0
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			n := 0
			for sc.Scan() {
				n++
				line := sc.Text()
				if strings.HasPrefix(strings.TrimSpace(line), "//") || !logCallRe.MatchString(line) {
					continue
				}
				scannedLogLines++
				m := tokenURLRe.FindString(line)
				if m == "" {
					continue
				}
				if m == "cloneURL" && !strings.Contains(filepath.ToSlash(path), "internal/runner/") {
					continue
				}
				// Passing the token to the scrubber is how it is kept out.
				if strings.Contains(line, "scrubToken(") || strings.Contains(line, "RedactURL(") || strings.Contains(line, "scrubSecret(") {
					continue
				}
				t.Errorf("%s:%d logs or formats a token-bearing value (%s): %s", path, n, m, strings.TrimSpace(line))
			}
			return sc.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scannedLogLines < 50 {
		t.Fatalf("scanned only %d logging lines — the walk is not seeing the sources", scannedLogLines)
	}
}
