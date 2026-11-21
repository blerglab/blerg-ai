package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type caseResult struct {
	Name, Status, Reason string
	Started, Finished    time.Time
	FaultAt, RecoveredAt time.Time
	Assertions           json.RawMessage
	Before, After        json.RawMessage
}
type campaignResult struct {
	Schema                    int
	NamespaceUID, Run, Status string
	Regression                string
	Reason                    string
	Cases                     []caseResult
}

func fixtureControl(k executor, action string, args ...string) (json.RawMessage, error) {
	command := []string{"exec", "restart-desktop-0", "-c", "fixture", "--", "/usr/local/bin/restart-drills", "control", action}
	command = append(command, args...)
	raw, err := k(command, nil)
	if err != nil {
		return nil, err
	}
	if !json.Valid(raw) {
		return nil, errors.New("invalid fixture receipt")
	}
	return raw, nil
}
func workload(k executor, kind, name, run string) (json.RawMessage, error) {
	raw, err := k([]string{"get", kind, name, "-o", "json"}, nil)
	if err != nil {
		return nil, err
	}
	var o struct {
		Metadata struct {
			UID    string
			Labels map[string]string
		}
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct{ Name, Image string }
				}
			}
		}
	}
	if json.Unmarshal(raw, &o) != nil || o.Metadata.UID == "" || o.Metadata.Labels[label] != run {
		return nil, errors.New("workload ownership mismatch")
	}
	return encode(object{"uid": o.Metadata.UID, "containers": o.Spec.Template.Spec.Containers}), nil
}
func patchWorkload(k executor, kind, name, run string, patch object) error {
	// Guard mutations using both label and Kubernetes resourceVersion.
	raw, err := k([]string{"get", kind, name, "-o", "json"}, nil)
	if err != nil {
		return err
	}
	var o struct {
		Metadata struct {
			UID, ResourceVersion string
			Labels               map[string]string
		}
	}
	if json.Unmarshal(raw, &o) != nil || o.Metadata.UID == "" || o.Metadata.ResourceVersion == "" || o.Metadata.Labels[label] != run {
		return errors.New("workload ownership mismatch")
	}
	mapAt(patch, "metadata")["resourceVersion"] = o.Metadata.ResourceVersion
	_, err = k([]string{"patch", kind, name, "--type=strategic", "-p", string(encode(patch))}, nil)
	return err
}
func imagesPatch(name, image string) object {
	return object{"spec": object{"template": object{"spec": object{"containers": []any{object{"name": name, "image": image}}}}}}
}
func awaitControl(ctx context.Context, k executor, action string) (json.RawMessage, error) {
	var result json.RawMessage
	err := until(ctx, func() (bool, error) { var err error; result, err = fixtureControl(k, action); return err == nil, nil })
	return result, err
}
func campaign(c config, j journal, k executor) error {
	k = ownedExec(k, j.Run)
	if err := c.validateInstall(); err != nil {
		return err
	}
	// Never overwrite a previous campaign, nor retry a partly completed upgrade silently.
	if err := reserveCampaign(c.State); err != nil {
		return err
	}
	result := campaignResult{Schema: 1, NamespaceUID: j.UID, Run: j.Run, Status: "running", Regression: "not-run"}
	for _, name := range cases {
		result.Cases = append(result.Cases, caseResult{Name: name, Status: "not-run"})
	}
	record := func() error { return save(c.State, "results.json", result) }
	if err := record(); err != nil {
		return err
	}
	fail := func(reason string) error {
		result.Status = "fail"
		result.Reason = reason
		_ = record()
		return errors.New(reason)
	}
	if err := ready(k); err != nil {
		return fail("installation not ready")
	}
	// Reuse the nine credential-free session-suite cases inside this isolated image/DB.
	if _, err := k([]string{"exec", "restart-desktop-0", "-c", "fixture", "--", "go", "run", "./cmd/session-suite", "-logs", "/data/session-regression"}, nil); err != nil {
		result.Regression = "fail"
		return fail("session regression prerequisite failed; private evidence retained in fixture volume")
	}
	regression, err := fixtureControl(k, "regression")
	if err != nil {
		return fail("missing regression receipts")
	}
	if err = save(c.State, "regression.json", regression); err != nil {
		return err
	}
	result.Regression = "pass"
	if err := record(); err != nil {
		return err
	}
	for index, name := range cases {
		r := caseResult{Name: name, Status: "running", Started: time.Now().UTC()}
		result.Cases[index] = r
		if err := record(); err != nil {
			return err
		}
		err := runCase(c, j, k, &r)
		r.Finished = time.Now().UTC()
		if err == nil {
			r.Status = "pass"
		} else {
			r.Reason = err.Error()
		}
		result.Cases[index] = r
		if e := record(); e != nil {
			return e
		}
		if err != nil {
			return fail("restart drill failed: " + name)
		}
	}
	result.Status = "pass"
	return record()
}
func runCase(c config, j journal, k executor, r *caseResult) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	before, err := workload(k, "deployment", "blerg-runner", j.Run)
	if err != nil {
		return err
	}
	beforePods, err := podEvidence(k, j)
	if err != nil {
		return err
	}
	r.Before = encode(object{"workload": before, "pods": beforePods})
	if _, err = fixtureControl(k, "begin", r.Name); err != nil {
		return errors.New("owned session start failed")
	}
	armed, err := awaitControl(ctx, k, "armed")
	if err != nil {
		return errors.New("mid-turn barrier missing")
	}
	r.Before = encode(object{"workload": before, "pods": beforePods, "barrier": armed})
	var old struct {
		Boot   string `json:"daemon_boot"`
		Engine string `json:"engine"`
	}
	if json.Unmarshal(armed, &old) != nil || old.Boot == "" {
		return errors.New("missing daemon boot receipt")
	}
	r.FaultAt = time.Now().UTC()
	switch r.Name {
	case "daemon":
		if _, err = fixtureControl(k, "kill"); err != nil {
			return errors.New("daemon fault failed")
		}
	case "server":
		err = patchWorkload(k, "deployment", "blerg-runner", j.Run, object{"spec": object{"template": object{"metadata": object{"annotations": object{"acceptance.blerg.dev/restart": random()}}}}})
	case "upgrade":
		for _, p := range []struct{ name, image string }{{"blerg-core", c.Core}, {"blerg-board", c.Board}, {"blerg-runner", c.Runner}} {
			if err = patchWorkload(k, "deployment", p.name, j.Run, imagesPatch(p.name, p.image)); err != nil {
				return err
			}
		}
		err = patchWorkload(k, "statefulset", "restart-desktop", j.Run, imagesPatch("daemon", c.Daemon))
	default:
		return errors.New("unknown fault")
	}
	if err != nil {
		return err
	}
	if err = ready(k); err != nil {
		return errors.New("fault recovery rollout failed")
	}
	if r.Name == "server" {
		raw, e := fixtureControl(k, "identity")
		if e != nil {
			return errors.New("engine identity unavailable after server restart")
		}
		var retained struct {
			Boot   string `json:"daemon_boot"`
			Engine string `json:"engine"`
		}
		if json.Unmarshal(raw, &retained) != nil || retained.Boot != old.Boot || retained.Engine == "" || retained.Engine != old.Engine {
			return errors.New("server restart replaced the running engine")
		}
	}
	if r.Name != "server" {
		err = until(ctx, func() (bool, error) {
			raw, e := fixtureControl(k, "identity")
			if e != nil {
				return false, nil //nolint:nilerr // Fixture pod is expected to be unavailable during a rollout; retry to the case deadline.
			}
			var next struct {
				Boot string `json:"daemon_boot"`
			}
			if json.Unmarshal(raw, &next) != nil {
				return false, errors.New("invalid boot receipt")
			}
			return next.Boot != "" && next.Boot != old.Boot, nil
		})
		if err != nil {
			return errors.New("daemon did not restart")
		}
		if _, err = fixtureControl(k, "recover"); err != nil {
			return errors.New("automatic session recovery failed")
		}
	}
	if _, err = fixtureControl(k, "release"); err != nil {
		return errors.New("barrier release failed")
	}
	r.Assertions, err = awaitControl(ctx, k, "verify")
	if err != nil {
		return errors.New("recovery assertions failed")
	}
	r.RecoveredAt = time.Now().UTC()
	if err = assertions(r.Assertions); err != nil {
		return err
	}
	r.After, err = workload(k, "deployment", "blerg-runner", j.Run)
	if err != nil {
		return err
	}
	var inventory []object
	err = until(ctx, func() (bool, error) {
		next, e := podEvidence(k, j)
		if e != nil {
			return false, e
		}
		proofReady := replacementProof(beforePods, next, r.Name) == nil
		if proofReady {
			inventory = next
		}
		return proofReady, nil
	})
	if err != nil {
		return errors.New("server replacement or upgrade image proof incomplete")
	}
	identity, err := fixtureControl(k, "identity")
	if err != nil {
		return err
	}
	r.After = encode(object{"workload": r.After, "pods": inventory, "identity": identity})
	fmt.Println(r.Name + ": PASS")
	return nil
}
func podEvidence(k executor, j journal) ([]object, error) {
	raw, err := k([]string{"get", "pods", "-l", label + "=" + j.Run, "-o", "json"}, nil)
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				UID, Name string
				Labels    map[string]string
			}
			Status struct {
				ContainerStatuses []struct{ Name, ImageID string }
			}
		}
	}
	if json.Unmarshal(raw, &list) != nil {
		return nil, errors.New("invalid pod inventory")
	}
	var evidence []object
	for _, pod := range list.Items {
		if pod.Metadata.Labels[label] != j.Run {
			return nil, errors.New("pod ownership mismatch")
		}
		for _, container := range pod.Status.ContainerStatuses {
			if container.ImageID == "" {
				return nil, errors.New("missing immutable image ID")
			}
			evidence = append(evidence, object{"pod_uid": pod.Metadata.UID, "container": container.Name, "image_id": container.ImageID})
		}
	}
	if len(evidence) < 4 {
		return nil, errors.New("incomplete image inventory")
	}
	return evidence, nil
}

func reserveCampaign(dir string) error {
	return os.Mkdir(filepath.Join(dir, "campaign-reserved"), 0700)
}

func assertions(raw json.RawMessage) error {
	var proof map[string]bool
	if json.Unmarshal(raw, &proof) != nil {
		return errors.New("invalid assertion receipt")
	}
	for _, key := range []string{"same_session", "durable_prefix", "marker_retained", "one_commit", "one_successful_turn", "writer_lock"} {
		if !proof[key] {
			return errors.New("missing recovery assertion: " + key)
		}
	}
	return nil
}

func replacementProof(before, after []object, name string) error {
	find := func(rows []object, container, key string) string {
		for _, r := range rows {
			if r["container"] == container {
				v, _ := r[key].(string)
				return v
			}
		}
		return ""
	}
	if name == "server" || name == "upgrade" {
		old := find(before, "blerg-runner", "pod_uid")
		next := find(after, "blerg-runner", "pod_uid")
		if old == "" || next == "" || old == next {
			return errors.New("server pod replacement not proven")
		}
		for _, r := range after {
			if r["container"] == "blerg-runner" && r["pod_uid"] == old {
				return errors.New("old server pod still present")
			}
		}
	}
	if name == "upgrade" {
		for _, container := range []string{"blerg-runner", "daemon"} {
			old := find(before, container, "image_id")
			next := find(after, container, "image_id")
			if old == "" || next == "" || old == next {
				return errors.New("upgrade image identity unchanged")
			}
		}
	}
	return nil
}

func ownedExec(k executor, run string) executor {
	return func(args []string, input []byte) ([]byte, error) {
		if len(args) > 1 && args[0] == "exec" {
			if args[1] != "restart-desktop-0" {
				return nil, errors.New("exec target is not the fixture pod")
			}
			raw, err := k([]string{"get", "pod", args[1], "-o", "json"}, nil)
			if err != nil {
				return nil, err
			}
			var pod struct {
				Metadata struct {
					UID    string
					Labels map[string]string
				}
			}
			if json.Unmarshal(raw, &pod) != nil || pod.Metadata.UID == "" || pod.Metadata.Labels[label] != run {
				return nil, errors.New("fixture pod ownership mismatch")
			}
		}
		return k(args, input)
	}
}
