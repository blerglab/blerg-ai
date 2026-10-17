package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/models"
)

var (
	ErrClusterCodexCatalogAuth = errors.New("cluster Codex is not authenticated. Reconnect Codex in Settings and retry")
	ErrClusterCodexCatalog     = errors.New("could not load Cluster Codex models. Check the Cluster Codex credential and try again")
)

const clusterCodexCatalogTTL = time.Minute
const clusterCodexCatalogMaxOutput = 1 << 20

// The cache is scoped to the account, authorising browser session/token, exact
// credential and agent image. A workstation's CLI catalog is never used here.
type clusterCodexCatalogCache struct {
	mu      sync.Mutex
	entries map[[32]byte]clusterCodexCatalogEntry
	running map[[32]byte]chan struct{}
	slots   chan struct{}
}
type clusterCodexCatalogEntry struct {
	list  models.List
	err   error
	until time.Time
}

// CodexModels asks the actual Cluster agent image for the launching account's
// model catalog. Credentials follow the same personal-first/operator-fallback
// rule as session Jobs. No credential is returned to the browser or logged.
func (j *JobManager) CodexModels(ctx context.Context, accountID string, proof coreProof) (models.List, error) {
	return j.codexModels(ctx, accountID, proof, false)
}

func (j *JobManager) codexModels(ctx context.Context, accountID string, proof coreProof, force bool) (models.List, error) {
	if j == nil || j.Image == "" {
		return models.List{}, ErrClusterCodexCatalog
	}
	if accountID == "" || !proof.valid() {
		return models.List{}, errNoLivenessProof
	}
	credential, err := j.codexCatalogCredential(ctx, accountID, proof)
	if err != nil {
		return models.List{}, err
	}
	return j.codexModelsWithCredential(ctx, accountID, proof, credential, force)
}

// Operator discovery never looks up or impersonates an account.
func (j *JobManager) codexOperatorModels(ctx context.Context, force bool) (models.List, error) {
	if j == nil || j.Image == "" {
		return models.List{}, ErrClusterCodexCatalog
	}
	credential, err := j.codexOperatorCatalogCredential(ctx)
	if err != nil {
		return models.List{}, err
	}
	return j.codexModelsWithCredential(ctx, "operator", coreProof{}, credential, force)
}

func (j *JobManager) codexModelsWithCredential(ctx context.Context, accountID string, proof coreProof, credential []byte, force bool) (models.List, error) {
	key := sha256.Sum256([]byte(accountID + "\x00" + proof.TokenID + "\x00" + proof.SessionID + "\x00" + j.Image + "\x00" + string(credential)))
	c := &j.codexCatalog
	for {
		c.mu.Lock()
		if c.entries == nil {
			c.entries = make(map[[32]byte]clusterCodexCatalogEntry)
			c.running = make(map[[32]byte]chan struct{})
			c.slots = make(chan struct{}, 2)
		}
		if force {
			delete(c.entries, key)
			force = false
		}
		if e, ok := c.entries[key]; ok && time.Now().Before(e.until) {
			c.mu.Unlock()
			return cloneClusterModelList(e.list), e.err
		}
		if done, ok := c.running[key]; ok {
			c.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return models.List{}, ctx.Err()
			}
		}
		// Bound completed entries even when many accounts browse the launch sheet.
		for k, e := range c.entries {
			if time.Now().After(e.until) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= 128 {
			for k := range c.entries {
				delete(c.entries, k)
				break
			}
		}
		done := make(chan struct{})
		c.running[key] = done
		c.mu.Unlock()
		select {
		case c.slots <- struct{}{}:
		case <-ctx.Done():
			c.mu.Lock()
			delete(c.running, key)
			close(done)
			c.mu.Unlock()
			return models.List{}, ctx.Err()
		}
		snapshot, snapshotErr := codexProbeAuthSnapshot(credential, time.Now())
		var list models.List
		probeErr := snapshotErr
		if probeErr == nil {
			list, probeErr = j.probeClusterCodexModels(ctx, snapshot)
		}
		<-c.slots
		ttl := clusterCodexCatalogTTL
		if probeErr != nil {
			ttl = 10 * time.Second
		}
		c.mu.Lock()
		if ctx.Err() == nil {
			c.entries[key] = clusterCodexCatalogEntry{list: list, err: probeErr, until: time.Now().Add(ttl)}
		}
		delete(c.running, key)
		close(done)
		c.mu.Unlock()
		return cloneClusterModelList(list), probeErr
	}
}

func cloneClusterModelList(l models.List) models.List {
	if l.FetchedAt != nil {
		at := *l.FetchedAt
		l.FetchedAt = &at
	}
	l.EngineEfforts = append([]string{}, l.EngineEfforts...)
	l.Models = append([]models.Model{}, l.Models...)
	for i := range l.Models {
		l.Models[i].Efforts = append([]string{}, l.Models[i].Efforts...)
	}
	return l
}

func (j *JobManager) codexCatalogCredential(ctx context.Context, accountID string, proof coreProof) ([]byte, error) {
	credential, found, err := j.fetchPersonalCredential(ctx, accountID, "codex", proof)
	if errors.Is(err, errCoreManagedCredential) {
		access, managed, accessErr := fetchManagedAccess(ctx, j.CredentialClient, j.CoreURL, j.CoreInternalKey, managedAccessRequest{AccountID: accountID, Engine: "codex", TokenID: proof.TokenID, SessionID: proof.SessionID})
		if accessErr != nil || !managed {
			return nil, ErrClusterCodexCatalogAuth
		}
		// The transient catalog job receives access only, never refresh authority.
		return json.Marshal(access)
	}
	if err != nil {
		return nil, ErrClusterCodexCatalog
	}
	if found {
		return credential, nil
	}
	return j.codexOperatorCatalogCredential(ctx)
}

func (j *JobManager) codexOperatorCatalogCredential(ctx context.Context) ([]byte, error) {
	name := j.SecretName
	if j.OAuthSecretName != "" {
		name = j.OAuthSecretName
	}
	resp, err := j.doWithContentType(ctx, http.MethodGet, "/api/v1/namespaces/"+j.Namespace+"/secrets/"+url.PathEscape(name), nil, "application/json")
	if err != nil {
		return nil, ErrClusterCodexCatalog
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrClusterCodexCatalogAuth
	}
	if resp.StatusCode != http.StatusOK {
		return nil, ErrClusterCodexCatalog
	}
	var secret struct {
		Data map[string]string `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&secret) != nil {
		return nil, ErrClusterCodexCatalog
	}
	credential, err := base64.StdEncoding.DecodeString(secret.Data["CODEX_AUTH_JSON"])
	if err != nil || len(credential) == 0 {
		return nil, ErrClusterCodexCatalogAuth
	}
	return credential, nil
}

// codexProbeAuthSnapshot deliberately withholds refresh tokens. A transient
// catalog reader must not rotate the durable credential used by live sessions.
func codexProbeAuthSnapshot(raw []byte, now time.Time) ([]byte, error) {
	var managed managedAccess
	if json.Unmarshal(raw, &managed) == nil && managed.Mode == "chatgptAuthTokens" {
		if managed.AccessToken == "" || managed.AccountID == "" || !managed.ExpiresAt.After(now.Add(30*time.Second)) {
			return nil, ErrClusterCodexCatalogAuth
		}
		return json.Marshal(managed)
	}
	var auth struct {
		AuthMode    string    `json:"auth_mode"`
		LastRefresh time.Time `json:"last_refresh"`
		APIKey      string    `json:"OPENAI_API_KEY"`
		Tokens      *struct {
			AccessToken  string `json:"access_token"`
			IDToken      string `json:"id_token"`
			RefreshToken string `json:"refresh_token"`
			AccountID    string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &auth) != nil {
		return nil, ErrClusterCodexCatalogAuth
	}
	if auth.APIKey != "" {
		return json.Marshal(map[string]string{"auth_mode": "apikey", "OPENAI_API_KEY": auth.APIKey})
	}
	if auth.Tokens == nil || auth.Tokens.AccessToken == "" {
		return nil, ErrClusterCodexCatalogAuth
	}
	parts := strings.Split(auth.Tokens.AccessToken, ".")
	if len(parts) == 3 {
		claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil, ErrClusterCodexCatalogAuth
		}
		var claims struct {
			Exp int64 `json:"exp"`
		}
		if json.Unmarshal(claimsRaw, &claims) != nil {
			return nil, ErrClusterCodexCatalogAuth
		}
		if claims.Exp > 0 && !time.Unix(claims.Exp, 0).After(now.Add(30*time.Second)) {
			return nil, ErrClusterCodexCatalogAuth
		}
	}
	// LastRefresh is probe-local metadata. Without it native Codex attempts a
	// proactive refresh even for a valid access token. JWT expiry was checked
	// above; this short-lived snapshot must use the access token alone.
	auth.LastRefresh = now.UTC()
	if auth.AuthMode == "" {
		auth.AuthMode = "chatgpt"
	}
	auth.Tokens.RefreshToken = ""
	return json.Marshal(auth) //nolint:gosec // G117: access-only credential intentionally marshaled only into the private Kubernetes Secret.
}

// Override the agent entrypoint. A native model/list request forces a remote
// refresh; debug models alone can silently return its bundled fallback. Read
// only the fresh cache, so failed auth/network never advertises stale efforts.
// The snapshot has no refresh token, and no assistant turn is started.
const clusterCodexCatalogCommand = `set -eu
umask 077
export CODEX_HOME="$(mktemp -d)"
exec python3 - <<'PYPROBE'
import json, os, pathlib, select, subprocess, sys, time
with open("/var/run/codex-auth/auth.json", "rb") as source:
    auth = json.loads(source.read(32769))
managed = auth.get("mode") == "chatgptAuthTokens"
if not managed:
    target = pathlib.Path(os.environ["CODEX_HOME"]) / "auth.json"
    target.write_text(json.dumps(auth))
    target.chmod(0o600)
child = subprocess.Popen(["codex", "app-server"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, bufsize=0)
end = time.monotonic() + 30
limit = 1048576

def write(message):
    child.stdin.write((json.dumps(message) + "\n").encode())
    child.stdin.flush()

def response(request_id):
    while time.monotonic() < end:
        if not select.select([child.stdout], [], [], max(0, end-time.monotonic()))[0]:
            raise RuntimeError()
        line = child.stdout.readline(limit+1)
        if not line or len(line) > limit:
            raise RuntimeError()
        message = json.loads(line)
        if message.get("id") == request_id:
            if "error" in message:
                raise RuntimeError()
            return
    raise RuntimeError()

try:
    write({"id":1, "method":"initialize", "params":{"clientInfo":{"name":"blerg_model_catalog", "version":"1"}, "capabilities":{"experimentalApi":True}}})
    response(1)
    write({"method":"initialized"})
    if managed:
        write({"id":3, "method":"account/login/start", "params":{"type":"chatgptAuthTokens", "accessToken":auth["access_token"], "chatgptAccountId":auth["chatgpt_account_id"], "chatgptPlanType":auth.get("plan_type")}})
        response(3)
    write({"id":2, "method":"model/list", "params":{"limit":100, "includeHidden":False}})
    response(2)
    cache = pathlib.Path(os.environ["CODEX_HOME"]) / "models_cache.json"
    with cache.open("rb") as source:
        raw = source.read(limit+1)
    if len(raw) > limit:
        raise RuntimeError()
    catalog = json.loads(raw).get("models")
    if not isinstance(catalog, list):
        raise RuntimeError()
    sys.stdout.write(json.dumps({"models":catalog}))
except Exception:
    sys.exit(43)
finally:
    child.terminate()
    try:
        child.wait(timeout=2)
    except subprocess.TimeoutExpired:
        child.kill()
        child.wait()
PYPROBE`

func (j *JobManager) probeClusterCodexModels(parent context.Context, credential []byte) (models.List, error) {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	id := strings.ToLower(rand.Text())
	name := "blerg-codex-models-" + id
	secretName := "blerg-codex-models-auth-" + id
	jobCreated, secretCreated := false, false
	// Cleanup uses its own deadline even when a browser disconnects mid-probe.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
		defer cancel()
		var paths []string
		if jobCreated {
			paths = append(paths, "/apis/batch/v1/namespaces/"+j.Namespace+"/jobs/"+name)
		}
		if secretCreated {
			paths = append(paths, "/api/v1/namespaces/"+j.Namespace+"/secrets/"+secretName)
		}
		for _, path := range paths {
			resp, err := j.doWithContentType(cleanup, http.MethodDelete, path, map[string]any{"propagationPolicy": "Background"}, "application/json")
			if err == nil {
				_ = resp.Body.Close()
			}
		}
	}()
	labels := map[string]any{"app": "blerg-codex-models", "probe": id}
	job := map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": name, "namespace": j.Namespace, "labels": labels}, "spec": map[string]any{
		"backoffLimit": 0, "activeDeadlineSeconds": 45, "ttlSecondsAfterFinished": 60,
		"template": map[string]any{"metadata": map[string]any{"labels": labels}, "spec": map[string]any{
			"restartPolicy": "Never", "automountServiceAccountToken": false, "terminationGracePeriodSeconds": 1,
			"securityContext": map[string]any{"fsGroup": sessionAgentUID},
			"volumes":         []map[string]any{{"name": "auth", "secret": map[string]any{"secretName": secretName, "defaultMode": 0440}}},
			"containers":      []map[string]any{{"name": "models", "image": j.Image, "command": []string{"/bin/sh", "-c", clusterCodexCatalogCommand}, "env": []map[string]any{{"name": "HOME", "value": "/workspace"}, {"name": "CODEX_HOME", "value": "/workspace/.codex"}}, "securityContext": sessionContainerSecurityContext(), "volumeMounts": []map[string]any{{"name": "auth", "mountPath": "/var/run/codex-auth", "readOnly": true}}, "resources": map[string]any{"requests": map[string]string{"cpu": "100m", "memory": "128Mi"}, "limits": map[string]string{"cpu": "1", "memory": "512Mi"}}}},
		}},
	}}
	resp, err := j.doWithContentType(ctx, http.MethodPost, "/apis/batch/v1/namespaces/"+j.Namespace+"/jobs", job, "application/json")
	if err != nil {
		return models.List{}, ErrClusterCodexCatalog
	}
	var created struct {
		Metadata struct {
			UID string `json:"uid"`
		} `json:"metadata"`
	}
	jobCreated = resp.StatusCode == http.StatusCreated
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&created)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || decodeErr != nil || created.Metadata.UID == "" {
		return models.List{}, ErrClusterCodexCatalog
	}
	secret := map[string]any{"apiVersion": "v1", "kind": "Secret", "type": "Opaque", "metadata": map[string]any{"name": secretName, "namespace": j.Namespace, "labels": labels, "ownerReferences": []map[string]any{{"apiVersion": "batch/v1", "kind": "Job", "name": name, "uid": created.Metadata.UID, "controller": false, "blockOwnerDeletion": false}}}, "stringData": map[string]string{"auth.json": string(credential)}}
	// Never decode or log a Kubernetes rejection body: it may echo auth.json.
	resp, err = j.doWithContentType(ctx, http.MethodPost, "/api/v1/namespaces/"+j.Namespace+"/secrets", secret, "application/json")
	if err != nil {
		return models.List{}, ErrClusterCodexCatalog
	}
	secretCreated = resp.StatusCode == http.StatusCreated
	_ = resp.Body.Close()
	if !secretCreated {
		return models.List{}, ErrClusterCodexCatalog
	}

	for {
		pod, phase, exitCode, err := j.codexCatalogPod(ctx, id)
		if err != nil {
			return models.List{}, ErrClusterCodexCatalog
		}
		if phase == "Failed" {
			if exitCode == 42 {
				return models.List{}, ErrClusterCodexCatalogAuth
			}
			return models.List{}, ErrClusterCodexCatalog
		}
		if phase == "Succeeded" {
			resp, err := j.doWithContentType(ctx, http.MethodGet, "/api/v1/namespaces/"+j.Namespace+"/pods/"+url.PathEscape(pod)+"/log?container=models&limitBytes="+fmt.Sprint(clusterCodexCatalogMaxOutput+1), nil, "application/json")
			if err != nil {
				return models.List{}, ErrClusterCodexCatalog
			}
			raw, readErr := io.ReadAll(io.LimitReader(resp.Body, clusterCodexCatalogMaxOutput+1))
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK || readErr != nil || len(raw) > clusterCodexCatalogMaxOutput {
				return models.List{}, ErrClusterCodexCatalog
			}
			catalog, err := models.ParseCodexModels(raw)
			if err != nil || len(catalog) == 0 {
				return models.List{}, ErrClusterCodexCatalog
			}
			now := time.Now().UTC()
			return models.List{Models: catalog, Source: models.SourceLive, FetchedAt: &now}, nil
		}
		select {
		case <-ctx.Done():
			return models.List{}, ErrClusterCodexCatalog
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (j *JobManager) codexCatalogPod(ctx context.Context, id string) (string, string, int, error) {
	resp, err := j.doWithContentType(ctx, http.MethodGet, "/api/v1/namespaces/"+j.Namespace+"/pods?labelSelector="+url.QueryEscape("probe="+id), nil, "application/json")
	if err != nil {
		return "", "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", "", 0, ErrClusterCodexCatalog
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Phase             string `json:"phase"`
				ContainerStatuses []struct {
					State struct {
						Terminated *struct {
							ExitCode int `json:"exitCode"`
						} `json:"terminated"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&list); err != nil {
		return "", "", 0, err
	}
	if len(list.Items) == 0 {
		return "", "", 0, nil
	}
	pod := list.Items[0]
	exit := 0
	for _, c := range pod.Status.ContainerStatuses {
		if c.State.Terminated != nil {
			exit = c.State.Terminated.ExitCode
		}
	}
	return pod.Metadata.Name, pod.Status.Phase, exit, nil
}
