package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/codexauth"
)

var errCodexOperatorCredential = errors.New("the operator Codex credential could not be read")

func (j *JobManager) prepareCodexSnapshot(ctx context.Context, spec SessionJobSpec, data map[string][]byte) error {
	raw, supplied := data["CODEX_AUTH_JSON"]
	if !supplied && !spec.NoOperatorFallback {
		var err error
		raw, err = j.codexOperatorCredential(ctx)
		if err != nil {
			return err
		}
	}
	if len(raw) == 0 && !supplied {
		return nil
	}
	snapshot, err := codexauth.Snapshot(raw, time.Now())
	if err != nil {
		return err
	}
	data["CODEX_AUTH_JSON"] = snapshot
	return nil
}

// Only the server reads the operator bundle. Consumers get a fresh access-only
// per-launch Secret, so later operator rotation cannot inject refresh authority.
func (j *JobManager) codexOperatorCredential(ctx context.Context) ([]byte, error) {
	name := j.SecretName
	if j.OAuthSecretName != "" {
		name = j.OAuthSecretName
	}
	resp, err := j.doWithContentType(ctx, http.MethodGet, "/api/v1/namespaces/"+j.Namespace+"/secrets/"+url.PathEscape(name), nil, "application/json")
	if err != nil {
		return nil, errCodexOperatorCredential
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errCodexOperatorCredential
	}
	var secret struct {
		Data map[string]string `json:"data"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 || json.Unmarshal(raw, &secret) != nil {
		return nil, errCodexOperatorCredential
	}
	credential, err := base64.StdEncoding.DecodeString(secret.Data["CODEX_AUTH_JSON"])
	if err != nil {
		return nil, errCodexOperatorCredential
	}
	return credential, nil
}
