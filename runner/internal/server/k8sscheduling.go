package server

import (
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
)

func excludedNodesFromEnv() []string {
	raw := os.Getenv("BLERG_RUNNER_POD_EXCLUDE_NODES")
	if raw == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

var nodeHostname = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

var errJobScheduling = errors.New("invalid required pod scheduling policy")

// guardJobScheduling runs at the shared API submission boundary, so both
// session and model-catalog Jobs obey the same trusted operator policy. Each
// required OR term receives both AND requirements; appending a new OR term
// would silently allow scheduling on an excluded node. Existing affinity stays.
func (j *JobManager) guardJobScheduling(method, path string, body any) error {
	if len(j.ExcludedNodes) == 0 || method != http.MethodPost || path != "/apis/batch/v1/namespaces/"+j.Namespace+"/jobs" {
		return nil
	}
	deny := errJobScheduling
	for _, node := range j.ExcludedNodes {
		if !nodeHostname.MatchString(node) {
			return deny
		}
	}
	job, ok := body.(map[string]any)
	if !ok {
		return deny
	}
	spec, ok := job["spec"].(map[string]any)
	if !ok {
		return deny
	}
	template, ok := spec["template"].(map[string]any)
	if !ok {
		return deny
	}
	pod, ok := template["spec"].(map[string]any)
	if !ok {
		return deny
	}
	// Direct binding bypasses the scheduler's required affinity.
	if pod["nodeName"] != nil {
		return deny
	}
	child := func(parent map[string]any, key string) (map[string]any, bool) {
		if parent[key] == nil {
			parent[key] = map[string]any{}
		}
		m, ok := parent[key].(map[string]any)
		return m, ok
	}
	affinity, ok := child(pod, "affinity")
	if !ok {
		return deny
	}
	node, ok := child(affinity, "nodeAffinity")
	if !ok {
		return deny
	}
	required, ok := child(node, "requiredDuringSchedulingIgnoredDuringExecution")
	if !ok {
		return deny
	}
	// Normalize the already-constructed JSON shape without retaining aliases.
	raw := required["nodeSelectorTerms"]
	var terms []any
	switch v := raw.(type) {
	case nil:
		terms = []any{map[string]any{}}
	case []any:
		terms = v
	case []map[string]any:
		for _, term := range v {
			terms = append(terms, term)
		}
	default:
		return deny
	}
	if len(terms) == 0 {
		return deny
	}
	for _, raw := range terms {
		term, ok := raw.(map[string]any)
		if !ok {
			return deny
		}
		var expressions []any
		switch v := term["matchExpressions"].(type) {
		case nil:
		case []any:
			expressions = v
		case []map[string]any:
			for _, e := range v {
				expressions = append(expressions, e)
			}
		default:
			return deny
		}
		values := append([]string(nil), j.ExcludedNodes...)
		term["matchExpressions"] = append(expressions, map[string]any{"key": "kubernetes.io/hostname", "operator": "Exists"}, map[string]any{"key": "kubernetes.io/hostname", "operator": "NotIn", "values": values})
	}
	required["nodeSelectorTerms"] = terms
	return nil
}
