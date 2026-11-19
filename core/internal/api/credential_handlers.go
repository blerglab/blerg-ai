package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/blerglab/blerg-ai/contracts/engines"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

var credentialKindShape = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// Git providers use the same vault without being coding engines.
var gitCredentialKinds = []string{"github", "gitlab"}

// gitReadOnlyKindShape is BLG-460's one-token-per-owner shape: "git-readonly:<provider>:<owner>".
// Unlike the single-slot kinds above (one "github" credential per account), an account may hold
// many of these — one per GitHub/GitLab owner (a user or an organization) its cluster workers
// read from. This is deliberately loose (anything non-empty after the provider): the exact
// owner rules (length, allowed characters, ".." and trailing "." refusal, GitLab's "/"-separated
// group paths) live in normalizeGitReadOnlyOwner, which also reports precisely what's wrong — a
// single combined regex can't produce that per-provider, per-reason message.
var gitReadOnlyKindShape = regexp.MustCompile(`^git-readonly:(github|gitlab):.+$`)

// gitReadOnlyOwnerSegment matches one path segment of a git-readonly owner — a GitHub owner, or
// one segment of a GitLab group/subgroup path: starts with a letter or digit, then letters,
// digits, dots, hyphens or underscores.
var gitReadOnlyOwnerSegment = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// normalizeGitReadOnlyOwner validates and lowercases a git-readonly owner (BLG-460 security
// re-review finding 3): the owner is always lowercased before it's stored or looked up, so
// "Acme-Corp" and "acme-corp" are the same credential. "owner" here is already the part after
// "git-readonly:<provider>:". err explains exactly what's wrong, for direct display in the UI.
func normalizeGitReadOnlyOwner(provider, owner string) (string, error) {
	lower := strings.ToLower(owner)
	if strings.Contains(lower, "..") {
		return "", errors.New(`owner can't contain ".."`)
	}
	if strings.HasSuffix(lower, ".") {
		return "", errors.New(`owner can't end with a "."`)
	}
	switch provider {
	case "github":
		if lower == "" || len(lower) > 39 || !gitReadOnlyOwnerSegment.MatchString(lower) {
			return "", errors.New("GitHub owner must be 1-39 characters: letters, digits, dots, hyphens or underscores")
		}
	case "gitlab":
		if lower == "" || len(lower) > 255 {
			return "", errors.New("GitLab owner (or group path) must be 1-255 characters")
		}
		for _, seg := range strings.Split(lower, "/") {
			if seg == "" || !gitReadOnlyOwnerSegment.MatchString(seg) {
				return "", fmt.Errorf("GitLab owner path has an invalid segment %q", seg)
			}
		}
	default:
		return "", fmt.Errorf("unknown provider %q", provider)
	}
	return lower, nil
}

// splitGitReadOnlyKind parses "git-readonly:<provider>:<owner>" into its provider and (not yet
// normalized) owner. ok is false for anything else, including a kind that merely starts with
// "git-readonly:" but names an unknown provider.
func splitGitReadOnlyKind(kind string) (provider, owner string, ok bool) {
	m := gitReadOnlyKindShape.FindStringSubmatch(kind)
	if m == nil {
		return "", "", false
	}
	return m[1], strings.TrimPrefix(kind, "git-readonly:"+m[1]+":"), true
}

// canonicalGitReadOnlyKind validates and normalizes a "git-readonly:<provider>:<owner>" kind.
// matched is false when kind isn't shaped like a git-readonly kind at all (the caller should
// fall through to its other engine checks). matched is true with a non-nil err when it is one
// but the owner fails normalizeGitReadOnlyOwner's rules — err is the exact reason to show.
func canonicalGitReadOnlyKind(kind string) (canonical string, matched bool, err error) {
	provider, owner, ok := splitGitReadOnlyKind(kind)
	if !ok {
		return "", false, nil
	}
	normalized, err := normalizeGitReadOnlyOwner(provider, owner)
	if err != nil {
		return "", true, err
	}
	return "git-readonly:" + provider + ":" + normalized, true, nil
}

func validCredentialKind(kind string) bool {
	if slices.Contains(gitCredentialKinds, kind) {
		return true
	}
	if gitReadOnlyKindShape.MatchString(kind) {
		return true
	}
	if kind == "" {
		return false
	}
	descriptor, ok := engines.Lookup(kind)
	return ok && descriptor.CredentialImport
}

// storeCredentialRequest is POST /api/credentials's body. There is deliberately no account-id
// field here (and handleStoreCredential never reads one from the request) — the account acted
// on is always the caller's own, taken from the verified access token's Sub claim by
// requireHumanPrincipal/principalFromCtx, never from anything client-supplied (§5's critical
// security requirement).
type storeCredentialRequest struct {
	Engine     string `json:"engine"`
	Credential string `json:"credential"`
}

// handleStoreCredential encrypts and upserts the caller's own credential for one engine.
// Gated behind requireHumanPrincipal in router.go.
func (d Deps) handleStoreCredential(w http.ResponseWriter, r *http.Request) {
	if d.Credentials == nil {
		http.Error(w, "credentials service unavailable", http.StatusServiceUnavailable)
		return
	}
	principal, ok := principalFromCtx(r.Context())
	if !ok {
		http.Error(w, "missing principal", http.StatusUnauthorized)
		return
	}
	// Cap the body BEFORE decoding (audit M-4): an unbounded POST body would otherwise let any
	// authenticated caller force the server to buffer an arbitrarily large "credential" value.
	// 64 KiB is generous for any real API token/credential while still bounding the worst case.
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body storeCredentialRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if body.Engine == "" || body.Credential == "" {
		http.Error(w, "engine and credential are required", http.StatusBadRequest)
		return
	}
	if !validCredentialKind(body.Engine) {
		http.Error(w, "unknown engine", http.StatusBadRequest)
		return
	}
	engine := body.Engine
	if canonical, matched, ownerErr := canonicalGitReadOnlyKind(body.Engine); matched {
		if ownerErr != nil {
			http.Error(w, ownerErr.Error(), http.StatusBadRequest)
			return
		}
		provider, _, _ := splitGitReadOnlyKind(body.Engine)
		if err := d.validateGitReadOnlyToken(r.Context(), provider, body.Credential); err != nil {
			log.Printf("credentials: store: read-only %s token refused: %v", provider, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		engine = canonical
	}
	if err := d.Credentials.Store(r.Context(), principal.Sub, engine, []byte(body.Credential)); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// credentialSummaryResponse is what GET /api/credentials returns per credential — engine and
// last-updated timestamp only. Never ciphertext, never plaintext (§5).
type credentialSummaryResponse struct {
	Engine     string     `json:"engine"`
	UpdatedAt  string     `json:"updated_at"`
	Kind       string     `json:"kind"`
	State      string     `json:"state"`
	Generation string     `json:"generation"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	// LastUsedAt is the most recent time any component actually fetched this credential's
	// plaintext over the internal boundary (credential_access_log), not when it was stored —
	// BLG-460's read-only tokens for workers show this so the owner can tell a token is
	// actually in use. Never set for a credential that has never been fetched.
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// handleListCredentials lists the caller's own credentials (metadata only). Gated behind
// requireHumanPrincipal in router.go.
func (d Deps) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	if d.Credentials == nil {
		http.Error(w, "credentials service unavailable", http.StatusServiceUnavailable)
		return
	}
	principal, ok := principalFromCtx(r.Context())
	if !ok {
		http.Error(w, "missing principal", http.StatusUnauthorized)
		return
	}
	list, err := d.Credentials.List(r.Context(), principal.Sub)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]credentialSummaryResponse, 0, len(list))
	for _, c := range list {
		out = append(out, credentialSummaryResponse{Engine: c.Engine, UpdatedAt: c.UpdatedAt.Format(rfc3339Milli), Kind: c.Kind, State: c.State, Generation: c.Generation, ExpiresAt: c.ExpiresAt, LastUsedAt: c.LastUsedAt})
	}
	writeJSON(w, out)
}

// rfc3339Milli is the timestamp format used for credentialSummaryResponse.UpdatedAt.
const rfc3339Milli = "2006-01-02T15:04:05.000Z07:00"

// handleDeleteCredential removes the caller's own credential for {engine}. Gated behind
// requireHumanPrincipal in router.go. Deleting a nonexistent engine is a no-op 200, matching
// credentials.Service.Delete's idempotent-delete convention.
func (d Deps) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	if d.Credentials == nil {
		http.Error(w, "credentials service unavailable", http.StatusServiceUnavailable)
		return
	}
	principal, ok := principalFromCtx(r.Context())
	if !ok {
		http.Error(w, "missing principal", http.StatusUnauthorized)
		return
	}
	engine := r.PathValue("engine")
	if engine == "" {
		http.Error(w, "engine is required", http.StatusBadRequest)
		return
	}
	if canonical, matched, ownerErr := canonicalGitReadOnlyKind(engine); matched {
		if ownerErr != nil {
			http.Error(w, ownerErr.Error(), http.StatusBadRequest)
			return
		}
		// The owner is always lowercased before lookup, matching how it's stored — "Acme-Corp"
		// and "acme-corp" must delete the same row.
		engine = canonical
	} else if !credentialKindShape.MatchString(engine) {
		http.Error(w, "invalid engine", http.StatusBadRequest)
		return
	}
	// Registration may disappear before an owner removes an old connection.
	// Deletion grants no import authority. Unknown names require an existing
	// entry in this owner's vault, never a lookup in another account's state.
	if _, registered := engines.Lookup(engine); !registered && !slices.Contains(gitCredentialKinds, engine) {
		entries, err := d.Credentials.List(r.Context(), principal.Sub)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		found := false
		for _, entry := range entries {
			if entry.Engine == engine {
				found = true
				break
			}
		}
		if !found {
			http.Error(w, "unknown engine", http.StatusBadRequest)
			return
		}
	}
	if err := d.Credentials.Delete(r.Context(), principal.Sub, engine); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
