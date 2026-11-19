// restart-drills runs credential-free faults against a disposable installation.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const label = "acceptance.blerg.dev/restart-drill"

type config struct {
	Context, Namespace, Image, Postgres, State                                                          string
	PreviousCore, PreviousBoard, PreviousRunner, PreviousDaemon                                         string
	Core, Board, Runner, Daemon, CoreHost, BoardHost, RunnerHost, IngressClass, StorageClass, Templates string
}
type journal struct{ Context, Namespace, Run, UID, ConfigHash string }
type object = map[string]any
type executor func([]string, []byte) ([]byte, error)

var dns = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
var namespaceShape = regexp.MustCompile(`^acceptance-restart-[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)
var cases = []string{"daemon", "server", "upgrade"}

func load(get func(string) string) (config, error) {
	c := config{Context: get("BLERG_DRILL_CONTEXT"), Namespace: get("BLERG_DRILL_NAMESPACE"), Image: get("BLERG_DRILL_IMAGE"), Postgres: get("BLERG_DRILL_POSTGRES_IMAGE"), State: get("BLERG_DRILL_STATE"), PreviousCore: get("BLERG_DRILL_PREVIOUS_CORE_IMAGE"), PreviousBoard: get("BLERG_DRILL_PREVIOUS_BOARD_IMAGE"), PreviousRunner: get("BLERG_DRILL_PREVIOUS_RUNNER_IMAGE"), PreviousDaemon: get("BLERG_DRILL_PREVIOUS_DAEMON_IMAGE"), Core: get("BLERG_DRILL_CORE_IMAGE"), Board: get("BLERG_DRILL_BOARD_IMAGE"), Runner: get("BLERG_DRILL_RUNNER_IMAGE"), Daemon: get("BLERG_DRILL_DAEMON_IMAGE"), CoreHost: get("BLERG_DRILL_CORE_HOST"), BoardHost: get("BLERG_DRILL_BOARD_HOST"), RunnerHost: get("BLERG_DRILL_RUNNER_HOST"), IngressClass: get("BLERG_DRILL_INGRESS_CLASS"), StorageClass: get("BLERG_DRILL_STORAGE_CLASS"), Templates: get("BLERG_DRILL_TEMPLATES")}
	if c.Context == "" || strings.HasPrefix(c.Context, "-") || c.Image == "" || c.Postgres == "" || c.State == "" || !namespaceShape.MatchString(c.Namespace) {
		return c, errors.New("explicit context, acceptance-restart namespace, images and private state path required")
	}
	return c, nil
}
func (c config) validateInstall() error {
	for _, v := range []string{c.Image, c.Postgres, c.PreviousCore, c.PreviousBoard, c.PreviousRunner, c.PreviousDaemon, c.Core, c.Board, c.Runner, c.Daemon, c.IngressClass, c.StorageClass, c.Templates} {
		if v == "" || strings.ContainsAny(v, "\x00\r\n") {
			return errors.New("all install environment settings are required")
		}
	}
	classShape := regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)
	if !classShape.MatchString(c.StorageClass) || !classShape.MatchString(c.IngressClass) {
		return errors.New("invalid class name")
	}
	seen := map[string]bool{}
	for _, h := range []string{c.CoreHost, c.BoardHost, c.RunnerHost} {
		if !dns.MatchString(h) || !strings.HasPrefix(h, c.Namespace+".") && !strings.HasPrefix(h, c.Namespace+"-") || seen[h] {
			return errors.New("distinct namespace-prefixed ingress hostnames required")
		}
		if len(h) > 253 {
			return errors.New("ingress hostname too long")
		}
		for _, part := range strings.Split(h, ".") {
			if len(part) > 63 {
				return errors.New("ingress label too long")
			}
		}
		seen[h] = true
	}
	if c.Runner == c.PreviousRunner || c.Daemon == c.PreviousDaemon {
		return errors.New("upgrade requires different previous and candidate runner/daemon images")
	}
	return nil
}
func kubectl(c config) executor {
	return func(args []string, input []byte) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer cancel()
		requestTimeout := "--request-timeout=60s"
		if len(args) > 0 && args[0] == "exec" {
			requestTimeout = "--request-timeout=0"
		}
		//nolint:gosec // Operator context, fixed command vocabulary, no shell evaluation.
		cmd := exec.CommandContext(ctx, "kubectl", append([]string{"--context", c.Context, "--namespace", c.Namespace, requestTimeout}, args...)...)
		cmd.Stdin = bytes.NewReader(input)
		out, err := cmd.Output()
		if err != nil {
			return nil, errors.New("kubectl operation failed; inspect only the owned namespace (raw diagnostics withheld)")
		}
		return out, nil
	}
}
func encode(v any) []byte { b, _ := json.Marshal(v); return b }
func random() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("random source unavailable")
	}
	return hex.EncodeToString(b)
}
func meta(name string, c config, run string) object {
	return object{"name": name, "namespace": c.Namespace, "labels": object{label: run}}
}
func namespace(c config, run string) object {
	return object{"apiVersion": "v1", "kind": "Namespace", "metadata": object{"name": c.Namespace, "labels": object{label: run}}}
}
func owned(raw []byte, j journal) error {
	var n struct {
		Metadata struct {
			UID    string
			Labels map[string]string
		}
	}
	if json.Unmarshal(raw, &n) != nil || j.UID == "" || j.Run == "" || n.Metadata.UID != j.UID || n.Metadata.Labels[label] != j.Run {
		return errors.New("namespace ownership label/UID mismatch")
	}
	return nil
}
func save(dir, name string, v any) error {
	f, err := os.CreateTemp(dir, ".receipt-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(encode(v))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(dir, name))
}
func fingerprint(c config) string { sum := sha256.Sum256(encode(c)); return hex.EncodeToString(sum[:]) }
func verify(c config, k executor) (journal, error) {
	var j journal
	raw, err := os.ReadFile(filepath.Join(c.State, "owner.json"))
	if err != nil {
		return j, err
	}
	if json.Unmarshal(raw, &j) != nil || j.Context != c.Context || j.Namespace != c.Namespace {
		return j, errors.New("journal scope mismatch")
	}
	raw, err = k([]string{"get", "namespace", c.Namespace, "-o", "json"}, nil)
	if err != nil {
		return j, err
	}
	return j, owned(raw, j)
}
func execute(action string, c config, k executor) error {
	if action == "all" {
		for _, step := range []string{"up", "run", "down"} {
			if err := execute(step, c, k); err != nil {
				return err
			}
		}
		return nil
	}
	switch action {
	case "plan", "up", "run", "down":
	default:
		return errors.New("expected plan, up, run or down")
	}
	if action == "plan" {
		if err := c.validateInstall(); err != nil {
			return err
		}
		_, err := render(c, "render-only", true)
		if err == nil {
			fmt.Println("Plan valid: private install, three ingress hosts, scripted mid-turn daemon/server/upgrade cases; no cluster contact.")
		}
		return err
	}
	if action == "up" {
		if err := c.validateInstall(); err != nil {
			return err
		}
		// Render and validate before reserving state or contacting Kubernetes.
		run := random()
		resources, err := render(c, run, false)
		if err != nil {
			return err
		}
		if err = os.Mkdir(c.State, 0700); err != nil {
			return err
		}
		j := journal{Context: c.Context, Namespace: c.Namespace, Run: run, ConfigHash: fingerprint(c)}
		if err = save(c.State, "owner.json", j); err != nil {
			return err
		}
		if err = preflight(c, k); err != nil {
			return err
		}
		raw, err := k([]string{"create", "-f", "-", "-o", "json"}, encode(namespace(c, run)))
		if err != nil {
			return err
		}
		var n struct{ Metadata struct{ UID string } }
		if err = json.Unmarshal(raw, &n); err != nil {
			return errors.New("namespace creation receipt invalid")
		}
		j.UID = n.Metadata.UID
		if err = owned(raw, j); err != nil {
			return err
		}
		if err = save(c.State, "owner.json", j); err != nil {
			return err
		}
		for _, o := range resources {
			if _, err = k([]string{"create", "-f", "-"}, encode(o)); err != nil {
				return err
			}
		}
		return ready(k)
	}
	j, err := verify(c, k)
	if err != nil {
		return err
	}
	if action == "down" {
		if _, err = k([]string{"delete", "--raw", "/api/v1/namespaces/" + c.Namespace, "-f", "-"}, encode(object{"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": object{"uid": j.UID}, "propagationPolicy": "Foreground"})); err != nil {
			return err
		}
		if _, err = k([]string{"wait", "--for=delete", "namespace/" + c.Namespace, "--timeout=600s"}, nil); err != nil {
			return err
		}
		return save(c.State, "teardown.json", object{"status": "deleted", "namespace_uid": j.UID, "label_checked": true, "at": time.Now().UTC()})
	}
	if j.ConfigHash != fingerprint(c) {
		return errors.New("install configuration differs from ownership journal")
	}
	return campaign(c, j, k)
}
func preflight(c config, k executor) error {
	raw, err := k([]string{"get", "storageclass", c.StorageClass, "-o", "json"}, nil)
	if err != nil {
		return err
	}
	var sc struct{ ReclaimPolicy string }
	if json.Unmarshal(raw, &sc) != nil || sc.ReclaimPolicy != "Delete" {
		return errors.New("explicit storage class must have Delete reclaim policy")
	}
	raw, err = k([]string{"get", "ingress", "--all-namespaces", "-o", "json"}, nil)
	if err != nil {
		return err
	}
	var inventory struct {
		Items []struct {
			Spec struct{ Rules []struct{ Host string } }
		}
	}
	if err = json.Unmarshal(raw, &inventory); err != nil {
		return errors.New("ingress inventory invalid")
	}
	for _, i := range inventory.Items {
		for _, r := range i.Spec.Rules {
			if r.Host == c.CoreHost || r.Host == c.BoardHost || r.Host == c.RunnerHost || strings.HasPrefix(r.Host, "*.") && (strings.HasSuffix(c.CoreHost, r.Host[1:]) || strings.HasSuffix(c.BoardHost, r.Host[1:]) || strings.HasSuffix(c.RunnerHost, r.Host[1:])) {
				return errors.New("ingress hostname already routed; refusing shared ingress")
			}
		}
	}
	return nil
}
func ready(k executor) error {
	for _, target := range []string{"deployment/blerg-core", "deployment/blerg-board", "deployment/blerg-runner", "statefulset/restart-desktop"} {
		if _, err := k([]string{"rollout", "status", target, "--timeout=600s"}, nil); err != nil {
			return err
		}
	}
	return nil
}
func main() {
	if filepath.Base(os.Args[0]) == "claude" {
		if err := engine(context.Background(), os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "scripted engine failed")
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "fixture", "engine", "daemon", "init-db", "control":
			if err := fixtureMain(os.Args[1:]); err != nil {
				fmt.Fprintln(os.Stderr, "fixture operation failed")
				os.Exit(1)
			}
			return
		}
	}
	c, err := load(os.Getenv)
	if err == nil {
		if len(os.Args) != 2 {
			err = errors.New("expected one action")
		} else {
			err = execute(os.Args[1], c, kubectl(c))
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
