package livedata

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

// SecretAuthority keeps the epoch outside Postgres without adding a mandatory
// persistent volume to the sign-in service. The Core-only account may get/update
// exactly one Secret; it cannot list, create, or access other credentials.
type SecretAuthority struct {
	HTTP  *http.Client
	URL   string
	Token func() (string, error)
}
type secretRecord struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name            string            `json:"name"`
		Namespace       string            `json:"namespace"`
		ResourceVersion string            `json:"resourceVersion"`
		Annotations     map[string]string `json:"annotations,omitempty"`
	} `json:"metadata"`
	Data map[string][]byte `json:"data,omitempty"`
	Type string            `json:"type,omitempty"`
}
type SecretAuthorityState struct {
	fenced  atomic.Bool
	backend *SecretAuthority
	boot    authorityFile
	Keys    *Keyring
}

func InClusterAuthority() (*SecretAuthority, error) {
	ca, e := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
	if e != nil {
		return nil, ErrUnavailable
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, ErrUnavailable
	}
	return &SecretAuthority{URL: "https://kubernetes.default.svc/api/v1/namespaces/blerg/secrets/blerg-live-data-issuer", HTTP: &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}}, Token: func() (string, error) {
		raw, e := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
		if e != nil || len(raw) > 16384 {
			return "", ErrUnavailable
		}
		return string(raw), nil
	}}, nil
}
func (s *SecretAuthority) request(parent context.Context, method string, body any) (secretRecord, error) {
	if s.HTTP == nil || s.Token == nil || s.URL == "" {
		return secretRecord{}, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	var raw []byte
	var e error
	if body != nil {
		raw, e = json.Marshal(body)
		if e != nil {
			return secretRecord{}, ErrUnavailable
		}
	}
	token, e := s.Token()
	if e != nil || token == "" {
		return secretRecord{}, ErrUnavailable
	}
	req, e := http.NewRequestWithContext(ctx, method, s.URL, bytes.NewReader(raw))
	if e != nil {
		return secretRecord{}, ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := *s.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, e := client.Do(req)
	if e != nil {
		return secretRecord{}, ErrUnavailable
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return secretRecord{}, ErrUnavailable
	}
	data, e := io.ReadAll(io.LimitReader(res.Body, 32769))
	if e != nil || len(data) > 32768 {
		return secretRecord{}, ErrUnavailable
	}
	var out secretRecord
	if json.Unmarshal(data, &out) != nil || out.APIVersion != "v1" || out.Kind != "Secret" || out.Metadata.Name != "blerg-live-data-issuer" || out.Metadata.Namespace != "blerg" || out.Metadata.ResourceVersion == "" {
		return secretRecord{}, ErrUnavailable
	}
	return out, nil
}
func (s *SecretAuthority) Start(ctx context.Context, allowInitial bool) (*SecretAuthorityState, error) {
	return s.start(ctx, allowInitial, false)
}

// Fence rotates the persisted authority by resource-version compare-and-swap.
// It is available only to the operator CLI, never registered on an HTTP mux.
func (s *SecretAuthority) Fence(ctx context.Context) (*SecretAuthorityState, error) {
	return s.start(ctx, false, true)
}

func (s *SecretAuthority) start(ctx context.Context, allowInitial, fence bool) (*SecretAuthorityState, error) {
	previous, e := s.request(ctx, "GET", nil)
	if e != nil {
		return nil, e
	}
	var old authorityFile
	raw := previous.Data["authority.json"]
	if len(raw) > 0 {
		if json.Unmarshal(raw, &old) != nil || old.Epoch < 1 || old.Revocations < 0 || !validAuthority(old) || !keyID.MatchString(old.KeyID) {
			return nil, ErrUnavailable
		}
	} else if !allowInitial || previous.Metadata.Annotations["blerg.ai/authority-initialized"] == "true" {
		return nil, ErrUnavailable
	}
	if old.Epoch > 0 && !fence {
		return &SecretAuthorityState{backend: s, boot: old, Keys: authorityKeys(old)}, nil
	}
	if old.Epoch == math.MaxInt64 {
		return nil, ErrUnavailable
	}
	_, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return nil, ErrUnavailable
	}
	var id [16]byte
	if _, e = rand.Read(id[:]); e != nil {
		return nil, ErrUnavailable
	}
	next := authorityFile{Epoch: old.Epoch + 1, KeyID: hex.EncodeToString(id[:]), Private: key, Revocations: old.Revocations}
	encoded, e := json.Marshal(next)
	if e != nil {
		return nil, ErrUnavailable
	}
	previous.Data = map[string][]byte{"authority.json": encoded}
	if previous.Metadata.Annotations == nil {
		previous.Metadata.Annotations = map[string]string{}
	}
	previous.Metadata.Annotations["blerg.ai/authority-initialized"] = "true"
	if _, e = s.request(ctx, "PUT", previous); e != nil {
		return nil, e
	}
	return &SecretAuthorityState{backend: s, boot: next, Keys: &Keyring{epoch: next.Epoch, active: next.KeyID, private: key, previous: map[string]retiredKey{}}}, nil
}
func (s *SecretAuthorityState) Epoch() int64 {
	if s.fenced.Load() {
		return 0
	}
	current, e := s.backend.request(context.Background(), "GET", nil)
	if e != nil {
		return 0
	}
	var state authorityFile
	if json.Unmarshal(current.Data["authority.json"], &state) != nil {
		return 0
	}
	if state.Epoch != s.boot.Epoch || state.KeyID != s.boot.KeyID || !bytes.Equal(state.Private, s.boot.Private) {
		s.fenced.Store(true)
		return 0
	}
	return state.Epoch
}
