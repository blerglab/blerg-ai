package main

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func mapAt(o object, key string) object {
	if v, ok := o[key].(map[string]any); ok {
		return v
	}
	v := object{}
	o[key] = v
	return v
}
func service(name string, port int, c config, run string) object {
	return object{"apiVersion": "v1", "kind": "Service", "metadata": meta(name, c, run), "spec": object{"selector": object{"app": name}, "ports": []any{object{"port": port, "targetPort": port}}}}
}
func claim(name string, c config, run string) object {
	return object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": meta(name, c, run), "spec": object{"accessModes": []string{"ReadWriteOnce"}, "storageClassName": c.StorageClass, "resources": object{"requests": object{"storage": "1Gi"}}}}
}
func secretEnv(name, key string) object {
	return object{"name": name, "valueFrom": object{"secretKeyRef": object{"name": "blerg-secrets", "key": key}}}
}
func env(name, value string) object { return object{"name": name, "value": value} }
func resources() object {
	return object{"requests": object{"cpu": "100m", "memory": "128Mi"}, "limits": object{"cpu": "1", "memory": "1Gi"}}
}
func render(c config, run string, dry bool) ([]object, error) {
	password := random()
	control := random()
	key := encryptionKey()
	secrets := object{"POSTGRES_PASSWORD": password, "BLERG_CORE_LOCAL_KEY": key, "BLERG_BOARD_SECRET_KEY": encryptionKey(), "ANTHROPIC_API_KEY": "", "BLERG_CORE_GITHUB_TOKEN": "", "BLERG_RUNNER_GITHUB_TOKEN": "", "DRILL_CONTROL_KEY": control}
	for _, k := range []string{"BLERG_CORE_REGISTER_KEY", "BLERG_CORE_INTERNAL_KEY", "BLERG_CORE_BOARD_MINT_KEY", "BLERG_BOARD_SERVICE_KEY", "BLERG_RUNNER_KEY", "BLERG_RUNNER_DAEMON_TOKEN"} {
		secrets[k] = random()
	}
	for k, db := range map[string]string{"DATABASE_URL_CORE": "blerg_core", "DATABASE_URL_BOARD": "blerg_board", "BLERG_RUNNER_DATABASE_URL": "blerg_runner", "DRILL_ADMIN_DATABASE_URL": "postgres"} {
		secrets[k] = "postgres://blerg:" + password + "@restart-postgres:5432/" + db + "?sslmode=disable"
	}
	if dry {
		for k := range secrets {
			secrets[k] = "RENDER-ONLY"
		}
	}
	out := []object{{"apiVersion": "v1", "kind": "Secret", "metadata": meta("blerg-secrets", c, run), "type": "Opaque", "stringData": secrets}, claim("blerg-runner-data", c, run), claim("restart-desktop-data", c, run)}
	pg := object{"name": "postgres", "image": c.Postgres, "env": []any{env("POSTGRES_USER", "blerg"), env("POSTGRES_DB", "postgres"), secretEnv("POSTGRES_PASSWORD", "POSTGRES_PASSWORD")}, "resources": resources(), "volumeMounts": []any{object{"name": "db", "mountPath": "/var/lib/postgresql/data"}}, "readinessProbe": object{"exec": object{"command": []string{"pg_isready", "-U", "blerg", "-d", "postgres"}}, "periodSeconds": 2}}
	out = append(out, object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": meta("restart-postgres", c, run), "spec": object{"replicas": 1, "strategy": object{"type": "Recreate"}, "selector": object{"matchLabels": object{"app": "restart-postgres"}}, "template": object{"metadata": object{"labels": object{"app": "restart-postgres", label: run}}, "spec": object{"automountServiceAccountToken": false, "containers": []any{pg}, "volumes": []any{object{"name": "db", "emptyDir": object{}}}}}}}, service("restart-postgres", 5432, c, run))
	for _, file := range []string{"configmap.yaml", "core-deployment.yaml", "board-deployment.yaml", "runner-deployment.yaml"} {
		f, err := os.Open(filepath.Join(c.Templates, file)) //nolint:gosec // Operator-selected source directory; filename comes from the fixed production template list.
		if err != nil {
			return nil, err
		}
		decoder := yaml.NewDecoder(f)
		for {
			var o object
			err = decoder.Decode(&o)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				_ = f.Close()
				return nil, errors.New("invalid production template")
			}
			if len(o) == 0 {
				continue
			}
			md := mapAt(o, "metadata")
			md["namespace"] = c.Namespace
			mapAt(md, "labels")[label] = run
			switch o["kind"] {
			case "ServiceAccount", "Service":
			case "ConfigMap":
				data := mapAt(o, "data")
				for k, v := range data {
					if s, ok := v.(string); ok && strings.Contains(s, "${") {
						data[k] = ""
					}
				}
				for k, v := range map[string]string{"BLERG_CORE_URL": "http://blerg-core:8080", "BLERG_CORE_PUBLIC_URL": "http://" + c.CoreHost, "BLERG_BOARD_SELF_URL": "http://" + c.BoardHost, "BLERG_BOARD_PUBLIC_URL": "http://" + c.BoardHost, "BLERG_BOARD_AGENT_URL": "http://blerg-board:8080", "BLERG_RUNNER_URL": "http://blerg-runner:8080", "RUNNER_UI_BASE": "http://" + c.RunnerHost + "/sessions", "BLERG_RUNNER_SELF_URL": "http://" + c.RunnerHost, "BLERG_RUNNER_BOARD_URL": "http://blerg-board:8080", "BLERG_RUNNER_AGENT_IMAGE": "", "BLERG_RUNNER_AGENT_NAMESPACE": c.Namespace, "BLERG_RUNNER_MODEL_CATALOG_URL": "", "BLERG_RUNNER_MCP_GW_PUBLIC": "off", "BLERG_RUNNER_TRUSTED_PROXIES": "", "BLERG_RUNNER_WEBHOOK_ALLOW_PRIVATE": "false", "BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS": "false"} {
					data[k] = v
				}
			case "Deployment":
				spec := mapAt(o, "spec")
				spec["replicas"] = 1
				spec["strategy"] = object{"type": "Recreate"}
				template := mapAt(spec, "template")
				mapAt(mapAt(template, "metadata"), "labels")[label] = run
				pod := mapAt(template, "spec")
				pod["automountServiceAccountToken"] = false
				delete(pod, "affinity")
				pod["initContainers"] = []any{object{"name": "private-databases", "image": c.Image, "command": []string{"/usr/local/bin/restart-drills", "init-db"}, "env": []any{secretEnv("DRILL_ADMIN_DATABASE_URL", "DRILL_ADMIN_DATABASE_URL")}, "resources": resources()}}
				containers, _ := pod["containers"].([]any)
				for _, v := range containers {
					container, ok := v.(map[string]any)
					if !ok {
						_ = f.Close()
						return nil, errors.New("invalid production container shape")
					}
					name, _ := container["name"].(string)
					switch name {
					case "blerg-core":
						container["image"] = c.PreviousCore
					case "blerg-board":
						container["image"] = c.PreviousBoard
					case "blerg-runner":
						container["image"] = c.PreviousRunner
					default:
						_ = f.Close()
						return nil, errors.New("unexpected production container")
					}
					container["resources"] = resources()
					entries, _ := container["env"].([]any)
					replacements := map[string]string{"BLERG_RUNNER_MODEL_CATALOG_URL": "", "BLERG_RUNNER_AGENT_IMAGE": "", "BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS": "false"}
					if name == "blerg-core" {
						origins := []string{"http://" + c.CoreHost, "http://" + c.BoardHost, "http://" + c.RunnerHost}
						replacements["BLERG_CORE_ALLOWED_RETURN_ORIGINS"] = strings.Join(origins, ",")
						replacements["BLERG_CORE_ORIGIN_AUDIENCES"] = origins[0] + "=blerg-core," + origins[1] + "=blerg-board," + origins[2] + "=blerg-runner"
						replacements["BLERG_CORE_COOKIE_SECURE"] = "false"
						replacements["BLERG_SETUP_ALLOW_LOCAL"] = "false"
					}
					for _, e := range entries {
						entry, ok := e.(map[string]any)
						if !ok {
							_ = f.Close()
							return nil, errors.New("invalid production environment shape")
						}
						entryName, _ := entry["name"].(string)
						if value, ok := replacements[entryName]; ok {
							entry["value"] = value
							delete(entry, "valueFrom")
							delete(replacements, entryName)
						}
					}
					for k, v := range replacements {
						entries = append(entries, env(k, v))
					}
					container["env"] = entries
				}
			default:
				_ = f.Close()
				return nil, errors.New("unexpected production resource kind")
			}
			if strings.Contains(string(encode(o)), "${") {
				_ = f.Close()
				return nil, errors.New("unresolved template setting")
			}
			out = append(out, o)
		}
		_ = f.Close()
	}
	sharedEnv := []any{secretEnv("DRILL_ADMIN_DATABASE_URL", "DRILL_ADMIN_DATABASE_URL"), secretEnv("DRILL_CONTROL_KEY", "DRILL_CONTROL_KEY"), secretEnv("TEST_DATABASE_URL", "DRILL_ADMIN_DATABASE_URL"), env("BLERG_TOOL_WORKERS", "1"), secretEnv("BLERG_CORE_INTERNAL_KEY", "BLERG_CORE_INTERNAL_KEY"), env("DRILL_CORE_ORIGIN", "http://"+c.CoreHost), env("DRILL_RUNNER_ORIGIN", "http://"+c.RunnerHost)}
	out = append(out, object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": meta("restart-desktop", c, run), "spec": object{"serviceName": "restart-desktop", "replicas": 1, "selector": object{"matchLabels": object{"app": "restart-desktop"}}, "template": object{"metadata": object{"labels": object{"app": "restart-desktop", label: run}}, "spec": object{"automountServiceAccountToken": false, "shareProcessNamespace": true, "securityContext": object{"runAsUser": 1000, "runAsGroup": 1000, "fsGroup": 1000}, "containers": []any{
		object{"name": "fixture", "image": c.Image, "readinessProbe": object{"httpGet": object{"path": "/health", "port": 8099}, "periodSeconds": 2}, "securityContext": object{"capabilities": object{"add": []string{"SYS_PTRACE"}}, "seccompProfile": object{"type": "Unconfined"}}, "command": []string{"/usr/local/bin/restart-drills", "fixture"}, "env": sharedEnv, "resources": resources(), "volumeMounts": []any{object{"name": "state", "mountPath": "/data"}}},
		object{"name": "daemon", "image": c.PreviousDaemon, "command": []string{"/usr/local/bin/restart-drills", "daemon"}, "env": sharedEnv, "resources": resources(), "securityContext": object{"capabilities": object{"add": []string{"SYS_PTRACE"}}, "seccompProfile": object{"type": "Unconfined"}}, "volumeMounts": []any{object{"name": "state", "mountPath": "/data"}}},
	}, "volumes": []any{object{"name": "state", "persistentVolumeClaim": object{"claimName": "restart-desktop-data"}}}}}}}, service("restart-desktop", 8099, c, run))
	rules := []any{}
	for host, name := range map[string]string{c.CoreHost: "blerg-core", c.BoardHost: "blerg-board", c.RunnerHost: "blerg-runner"} {
		rules = append(rules, object{"host": host, "http": object{"paths": []any{object{"path": "/", "pathType": "Prefix", "backend": object{"service": object{"name": name, "port": object{"number": 8080}}}}}}})
	}
	out = append(out, object{"apiVersion": "networking.k8s.io/v1", "kind": "Ingress", "metadata": meta("restart-drills", c, run), "spec": object{"ingressClassName": c.IngressClass, "rules": rules}}, object{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": meta("restart-egress", c, run), "spec": object{"podSelector": object{}, "policyTypes": []string{"Egress"}, "egress": []any{object{"to": []any{object{"podSelector": object{}}}}, object{"ports": []any{object{"protocol": "UDP", "port": 53}, object{"protocol": "TCP", "port": 53}}}}}})
	// Apply egress isolation before any workload can start.
	policy := out[len(out)-1]
	out = append([]object{policy}, out[:len(out)-1]...)
	return out, nil
}
func encryptionKey() string {
	sum := sha256.Sum256([]byte(random()))
	return base64.StdEncoding.EncodeToString(sum[:])
}
