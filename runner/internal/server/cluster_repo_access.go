package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
)

var errRepositoryUnreadable = errors.New("the cluster cannot read this repository with the selected git credential; check its owner/name and connect a git credential with repository read access in Settings, then retry")
var errRepositoryCheckUnavailable = errors.New("repository read access could not be checked right now; retry when the git service is available")
var errRepositoryAddress = errors.New("the cluster repository check needs a permitted HTTP(S) clone URL; use an HTTPS repository URL or the install's configured git base")

func clusterStartFailureStatus(err error) int {
	if errors.Is(err, errRepositoryUnreadable) || errors.Is(err, errRepositoryAddress) {
		return http.StatusUnprocessableEntity
	}
	return http.StatusServiceUnavailable
}

// Check the read side of Git's HTTP transport, rather than a metadata API that might be readable
// with a token that cannot fetch repository contents. No upstream body or URL enters an error.
func checkRepositoryRead(ctx context.Context, client *http.Client, cloneURL, token string) error {
	endpoint, err := url.Parse(cloneURL)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") {
		return errRepositoryAddress
	}
	originScheme, originHost := endpoint.Scheme, endpoint.Host
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/info/refs"
	endpoint.RawPath = ""
	endpoint.RawQuery = "service=git-upload-pack"
	transport := *client
	transport.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for redirects := 0; redirects < 5; redirects++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil) // #nosec G704 -- Production uses the netguard client below; parsed HTTP(S) URLs and redirects stay on the original origin.
		if err != nil {
			return errRepositoryAddress
		}
		req.Header.Set("Accept", "application/x-git-upload-pack-advertisement")
		req.Header.Set("User-Agent", "blerg-runner")
		// Like the pod's clone (authURL), a credential goes only over https: a plain-http base is
		// checked anonymously, exactly as the pod will clone it.
		if token != "" && endpoint.Scheme == "https" {
			req.SetBasicAuth(gitprovider.Default.TokenUserForHost(endpoint.Hostname()), token)
		}
		response, err := transport.Do(req) // #nosec G704 -- Production uses the netguard client below; parsed HTTP(S) URLs and redirects stay on the original origin.
		if err != nil {
			if errors.Is(err, netguard.ErrURLNotPermitted) || errors.Is(err, netguard.ErrBlockedAddress) {
				return errRepositoryAddress
			}
			return errRepositoryCheckUnavailable
		}
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			next, err := response.Location()
			_ = response.Body.Close()
			if err != nil || next.Scheme != originScheme || next.Host != originHost || next.User != nil || next.Fragment != "" {
				return errRepositoryUnreadable
			}
			if next.RawQuery == "" {
				next.RawQuery = "service=git-upload-pack"
			}
			endpoint = next
			continue
		}
		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone {
				return errRepositoryUnreadable
			}
			return errRepositoryCheckUnavailable
		}
		head, readErr := io.ReadAll(io.LimitReader(response.Body, 1024))
		_ = response.Body.Close()
		if readErr != nil {
			return errRepositoryCheckUnavailable
		}
		if bytes.HasPrefix(head, []byte("001e# service=git-upload-pack\n0000")) {
			return nil
		}
		// Static HTTP repositories advertise plain hash/ref lines, including an empty refs file.
		if !strings.HasPrefix(response.Header.Get("Content-Type"), "application/x-git-") {
			if len(head) == 0 && strings.HasPrefix(response.Header.Get("Content-Type"), "text/plain") {
				return nil
			}
			fields := strings.Fields(strings.SplitN(string(head), "\n", 2)[0])
			if len(fields) == 2 && (len(fields[0]) == 40 || len(fields[0]) == 64) && strings.HasPrefix(fields[1], "refs/") {
				if _, err := hex.DecodeString(fields[0]); err == nil {
					return nil
				}
			}
		}
		return errRepositoryUnreadable
	}
	return errRepositoryUnreadable
}

// Read the same shared Secret key the pod would use, and only when operator fallback is allowed.
func (j *JobManager) operatorGitToken(ctx context.Context) (string, error) {
	name := firstNonEmpty(j.OAuthSecretName, j.SecretName)
	if name == "" {
		return "", nil
	}
	response, err := j.doWithContentType(ctx, http.MethodGet, "/api/v1/namespaces/"+j.Namespace+"/secrets/"+url.PathEscape(name), nil, "application/json")
	if err != nil {
		return "", errRepositoryCheckUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if response.StatusCode != http.StatusOK {
		return "", errRepositoryCheckUnavailable
	}
	var secret struct {
		Data map[string]string `json:"data"`
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 || json.Unmarshal(raw, &secret) != nil {
		return "", errRepositoryCheckUnavailable
	}
	token, err := base64.StdEncoding.DecodeString(secret.Data[gitTokenKey])
	if err != nil {
		return "", errRepositoryCheckUnavailable
	}
	return string(token), nil
}

func (j *JobManager) precheckRepository(spec SessionJobSpec, cloneURL string, sessionData map[string][]byte, personalKeys map[string]bool, operatorOK bool) error {
	return j.precheckRepositoryContext(context.Background(), spec, cloneURL, sessionData, personalKeys, operatorOK)
}
func (j *JobManager) precheckRepositoryContext(parent context.Context, spec SessionJobSpec, cloneURL string, sessionData map[string][]byte, personalKeys map[string]bool, operatorOK bool) error {
	if spec.NoRepo || spec.NewRepo {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	token := string(sessionData[gitTokenKey])
	if caller, ok := spec.ExtraEnv[gitTokenKey]; ok {
		token = caller
	} else if !personalKeys[gitTokenKey] && operatorOK && !spec.NoOperatorGitToken && !spec.NoOperatorFallback {
		var err error
		token, err = j.operatorGitToken(ctx)
		if err != nil {
			return err
		}
	}
	if j.repositoryRead != nil {
		return j.repositoryRead(ctx, cloneURL, token)
	}
	policy := netguard.Policy{}
	// Only an explicitly configured base may name an internal or plain-HTTP git server.
	if base, err := url.Parse(j.ExplicitGitURLBase); err == nil && base.Hostname() != "" && base.Hostname() == urlHost(cloneURL) {
		policy.AllowPrivateHosts = []string{base.Hostname()}
		if base.Scheme == "http" {
			policy.AllowHTTPHosts = []string{base.Hostname()}
		}
	}
	client := policy.Client(10*time.Second, 1<<20)
	defer client.CloseIdleConnections()
	return checkRepositoryRead(ctx, client, cloneURL, token)
}
