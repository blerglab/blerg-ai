package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/contracts/livetls"
	"github.com/blerglab/blerg-ai/core/internal/livedata"
)

// This offline Job identity can update only the two leaf Secrets. It has no
// issuer-secret, pod, deployment, exec or grant authority. Never print API bodies.
type liveLeafJob struct {
	Client             *http.Client
	BaseURL, Authority string
	Token              func() (string, error)
}

func runLiveLeafJob(ctx context.Context) error {
	authority, e := livedata.InClusterAuthority()
	if e != nil {
		return leafJobRefuse("job_api_identity_unavailable")
	}
	job := liveLeafJob{Client: authority.HTTP, BaseURL: strings.TrimSuffix(authority.URL, "blerg-live-data-issuer"), Token: authority.Token, Authority: "/authority"}
	return job.run(ctx, time.Now())
}
func (j liveLeafJob) request(ctx context.Context, method, name string, body any) (map[string]json.RawMessage, error) {
	if j.Client == nil || j.Token == nil || j.BaseURL == "" {
		return nil, leafJobRefuse("job_config_invalid")
	}
	var raw []byte
	var e error
	if body != nil {
		raw, e = json.Marshal(body)
		if e != nil {
			return nil, leafJobRefuse("job_payload_invalid")
		}
	}
	token, e := j.Token()
	if e != nil || token == "" {
		return nil, leafJobRefuse("job_token_unavailable")
	}
	request, e := http.NewRequestWithContext(ctx, method, j.BaseURL+name, bytes.NewReader(raw)) // #nosec G704 -- fixed in-cluster API and two fixed Secret names, never a caller URL.
	if e != nil {
		return nil, leafJobRefuse("job_request_invalid")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	client := *j.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, e := client.Do(request) // #nosec G704 -- dedicated Kubernetes client; redirects disabled.
	if e != nil {
		return nil, leafJobRefuse("job_api_unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	raw, e = io.ReadAll(io.LimitReader(response.Body, 65537))
	if e != nil || len(raw) > 65536 || response.StatusCode != http.StatusOK {
		return nil, leafJobRefuse("job_api_refused")
	}
	var record map[string]json.RawMessage
	var version, kind string
	var metadata struct{ Name, Namespace, ResourceVersion string }
	if json.Unmarshal(raw, &record) != nil || json.Unmarshal(record["apiVersion"], &version) != nil || version != "v1" || json.Unmarshal(record["kind"], &kind) != nil || kind != "Secret" || json.Unmarshal(record["metadata"], &metadata) != nil || metadata.Name != name || metadata.Namespace != "blerg" || metadata.ResourceVersion == "" {
		return nil, leafJobRefuse("job_secret_identity_mismatch")
	}
	return record, nil
}
func (j liveLeafJob) run(parent context.Context, now time.Time) error {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	root, e := os.MkdirTemp("", "live-data-leaves-")
	if e != nil {
		return leafJobRefuse("job_workdir_unavailable")
	}
	defer func() { _ = os.RemoveAll(root) }()
	authority := filepath.Join(root, "authority")
	if os.Mkdir(authority, 0700) != nil {
		return leafJobRefuse("job_workdir_unavailable")
	}
	for _, name := range []string{"identity.json", "ca.pem", "key.pem"} {
		raw, err := os.ReadFile(filepath.Join(j.Authority, name))                                             // #nosec G304 -- fixed filenames in the Job-only authority mount.
		if err != nil || len(raw) > 16384 || os.WriteFile(filepath.Join(authority, name), raw, 0600) != nil { // #nosec G703 -- fixed filename in our private MkdirTemp directory, never request data.
			return leafJobRefuse("job_authority_unavailable")
		}
	}
	// Preserve the authority and UUIDs. Missing/partial CA input is never recreated.
	if livetls.ProvisionAt(root, now) != nil {
		return leafJobRefuse("job_authority_invalid")
	}
	records := map[string]map[string]json.RawMessage{}
	replacements := map[string]map[string][]byte{}
	for _, role := range []string{"core", "runner"} {
		name := "blerg-live-data-" + role
		record, err := j.request(ctx, "GET", name, nil)
		if err != nil {
			return err
		}
		var old map[string][]byte
		if json.Unmarshal(record["data"], &old) != nil || len(old) != 4 {
			return leafJobRefuse("job_leaf_invalid")
		}
		next := map[string][]byte{}
		for _, file := range []string{"identity.json", "ca.pem", "cert.pem", "key.pem"} {
			raw, err := os.ReadFile(filepath.Join(root, role, file)) // #nosec G304 -- freshly provisioned private temporary bundle.
			if err != nil {
				return leafJobRefuse("job_provisioning_failed")
			}
			next[file] = raw
		}
		var before, after livetls.Manifest
		if json.Unmarshal(old["identity.json"], &before) != nil || json.Unmarshal(next["identity.json"], &after) != nil || before != after || !bytes.Equal(old["ca.pem"], next["ca.pem"]) {
			return leafJobRefuse("job_leaf_scope_or_ca_mismatch")
		}
		records[name] = record
		replacements[name] = next
	}
	for _, role := range []string{"core", "runner"} {
		name := "blerg-live-data-" + role
		record := records[name]
		raw, err := json.Marshal(replacements[name])
		if err != nil {
			return leafJobRefuse("job_payload_invalid")
		}
		record["data"] = raw
		// PUT preserves resourceVersion. A conflict refuses; the next Job retries
		// against fresh state. Partial rotation is safe under the same CA and scope.
		if _, err = j.request(ctx, "PUT", name, record); err != nil {
			return err
		}
	}
	return nil
}

func leafJobRefuse(cause string) error {
	failurelog.Default.Event("live_data_tls_renewal_job:"+cause, slog.LevelWarn, "cause", cause)
	return livetls.ErrIdentity
}
