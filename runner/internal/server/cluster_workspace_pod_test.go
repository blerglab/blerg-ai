package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Opt-in confined to an explicitly owned acceptance namespace. Credentials stay
// in a private fixture file and are never logged; no pods/exec path is used.
func TestClusterWorkspaceRealPodWriterFence(t *testing.T) {
	configPath := os.Getenv("P3_TEST_KUBECONFIG_JSON")
	if configPath == "" {
		t.Skip("requires an owned disposable pod cluster")
	}

	ns, excluded, image := os.Getenv("P3_TEST_NAMESPACE"), os.Getenv("P3_TEST_EXCLUDE_NODES"), os.Getenv("P3_TEST_POD_IMAGE")
	if err := podAcceptanceSettings(ns, excluded, image); err != nil {
		t.Fatal(err)
	}
	var config struct {
		Clusters []struct {
			Cluster struct {
				CA string `json:"certificate-authority-data"`
			}
		} `json:"clusters"`
		Users []struct {
			User struct {
				Cert  string `json:"client-certificate-data"`
				Key   string `json:"client-key-data"`
				Token string `json:"token"`
			}
		} `json:"users"`
	}
	raw, e := os.ReadFile(configPath)
	if e != nil {
		t.Fatal("read private cluster fixture")
	}
	if json.Unmarshal(raw, &config) != nil || len(config.Clusters) != 1 || len(config.Users) != 1 {
		t.Fatal("invalid private cluster fixture")
	}
	ca, e := base64.StdEncoding.DecodeString(config.Clusters[0].Cluster.CA)
	if e != nil {
		t.Fatal("invalid fixture CA")
	}
	var certificates []tls.Certificate
	if config.Users[0].User.Token == "" {
		cert, e := base64.StdEncoding.DecodeString(config.Users[0].User.Cert)
		if e != nil {
			t.Fatal("invalid fixture certificate")
		}
		key, e := base64.StdEncoding.DecodeString(config.Users[0].User.Key)
		if e != nil {
			t.Fatal("invalid fixture key")
		}
		pair, e := tls.X509KeyPair(cert, key)
		if e != nil {
			t.Fatal("invalid fixture identity")
		}
		certificates = append(certificates, pair)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("invalid fixture CA")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, Certificates: certificates}}
	defer transport.CloseIdleConnections()
	j := &JobManager{BaseURL: os.Getenv("P3_TEST_KUBE_URL"), Namespace: ns, Token: config.Users[0].User.Token, ExcludedNodes: strings.Split(excluded, ","), Client: &http.Client{Transport: transport, Timeout: 10 * time.Second}}
	if j.BaseURL == "" {
		t.Fatal("fixture URL missing")
	}
	spec := SessionJobSpec{SessionID: newUUID(), Resume: true}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	path := "/apis/batch/v1/namespaces/" + j.Namespace + "/jobs"
	job := map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": j.jobName(spec.SessionID), "labels": map[string]string{"app": "blerg-runner-agent", "session": spec.SessionID}}, "spec": map[string]any{"backoffLimit": 0, "template": map[string]any{"metadata": map[string]any{"labels": map[string]string{"app": "blerg-runner-agent", "session": spec.SessionID}}, "spec": map[string]any{"restartPolicy": "Never", "automountServiceAccountToken": false, "containers": []map[string]any{{"name": "writer", "image": image, "imagePullPolicy": "IfNotPresent", "command": []string{"sleep", "300"}, "securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 1000, "allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []string{"ALL"}}}, "resources": map[string]any{"requests": map[string]string{"cpu": "50m", "memory": "32Mi"}, "limits": map[string]string{"cpu": "100m", "memory": "128Mi"}}}}}}}}
	create := func() string {
		t.Helper()
		resp, e := j.doWithContentType(ctx, http.MethodPost, path, job, "application/json")
		if e != nil {
			t.Fatal("fixture Job create failed")
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatal("fixture Job refused", resp.StatusCode)
		}
		var created historyClaim
		if json.NewDecoder(resp.Body).Decode(&created) != nil || created.Metadata.UID == "" {
			t.Fatal("fixture Job identity missing")
		}
		return created.Metadata.UID
	}
	first := create()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := j.fenceNativeHistoryWriter(cleanup, spec); err != nil {
			t.Error("fixture writer cleanup unproven")
		}
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	running := false
	for !running {
		resp, e := j.doWithContentType(ctx, http.MethodGet, "/api/v1/namespaces/"+j.Namespace+"/pods?labelSelector="+url.QueryEscape("session="+spec.SessionID), nil, "application/json")
		if e != nil {
			t.Fatal("fixture Pod list failed")
		}
		var list struct {
			Items []struct {
				Metadata struct {
					OwnerReferences []struct {
						UID string `json:"uid"`
					} `json:"ownerReferences"`
				} `json:"metadata"`
				Status struct {
					Phase string `json:"phase"`
				} `json:"status"`
			} `json:"items"`
		}
		err := json.NewDecoder(resp.Body).Decode(&list)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal("fixture Pod inventory failed")
		}
		for _, pod := range list.Items {
			if pod.Status.Phase == "Running" {
				for _, owner := range pod.Metadata.OwnerReferences {
					if owner.UID == first {
						running = true
					}
				}
			}
		}
		if !running {
			select {
			case <-ctx.Done():
				t.Fatal("fixture writer never started")
			case <-ticker.C:
			}
		}
	}
	if e := j.fenceNativeHistoryWriter(ctx, spec); e != nil {
		t.Fatal("old real Pod writer was not fenced", e)
	}
	next := create()
	if next == first {
		t.Fatal("replacement reused old Job identity")
	}
	if e := j.fenceNativeHistoryWriter(ctx, spec); e != nil {
		t.Fatal("replacement cleanup not proven", e)
	}
}

// Validate every target before reading credentials or constructing an API client.
func podAcceptanceSettings(ns, excluded, image string) error {
	if !regexp.MustCompile(`^acceptance-[a-z0-9]([a-z0-9-]*[a-z0-9])?$`).MatchString(ns) || len(ns) > 63 {
		return errors.New("explicit acceptance namespace required")
	}
	if excluded == "" {
		return errors.New("explicit excluded hostname labels required")
	}
	for _, node := range strings.Split(excluded, ",") {
		if !nodeHostname.MatchString(node) {
			return errors.New("invalid excluded hostname label")
		}
	}
	tail := image[strings.LastIndex(image, "/")+1:]
	parts := strings.Split(tail, ":")
	if len(parts) != 2 || parts[0] == "" || !regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`).MatchString(parts[1]) || parts[1] == "latest" || strings.Contains(image, "@") {
		return errors.New("explicit fixture image tag required")
	}
	return nil
}
func TestPodAcceptanceTargetValidation(t *testing.T) {
	for _, ns := range []string{"blerg", "blerg-runner-sessions", "default", "acceptance-", "acceptance-../other", strings.Repeat("x", 64)} {
		if podAcceptanceSettings(ns, "excluded-node", "fixture/image:tag") == nil {
			t.Fatal("unsafe pod target admitted")
		}
	}
	for _, image := range []string{"fixture/image", "registry.invalid:5000/image", "fixture/image:latest", "fixture/image@sha256:fixture"} {
		if podAcceptanceSettings("acceptance-fixture", "excluded-node", image) == nil {
			t.Fatal("untagged pod fixture admitted")
		}
	}
	if podAcceptanceSettings("acceptance-a", "excluded-node", "registry.invalid:5000/image:test-tag") != nil {
		t.Fatal("valid explicit fixture refused")
	}
}
