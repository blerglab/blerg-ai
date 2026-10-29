#!/usr/bin/env python3
"""Prepare/run an exclusively owned namespace. Dry run never invokes kubectl.

0.5.0 runs one real-pod happy-path smoke. The full matrix is deferred to #180.
Public sources are mirrored into a private namespace-local push fixture. No default context,
production namespace, exec, secret dump, node mutation or cluster-wide prune.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys
import tempfile
import time
import uuid
from urllib.parse import urlsplit

HERE = Path(__file__).resolve().parent
BASE = HERE.parent
LABEL = "blerg.acceptance/run"
SCHEMA = 1
RBAC_LIMITATION = "not-enforced: cluster authorization is AlwaysAllow (RBAC off)"


class Refusal(Exception):
    """Messages are fixed, credential-free diagnostics owned by this module.

    Never construct this exception from tool output, configuration or URLs.
    """


def exception_details(error):
    """Keep useful diagnostics without serializing commands, payloads or URLs."""
    if isinstance(error, Refusal):
        message = str(error)
    elif isinstance(error, json.JSONDecodeError):
        message = f"invalid JSON at line {error.lineno}, column {error.colno} (input redacted)"
    elif isinstance(error, subprocess.TimeoutExpired):
        message = "subprocess timed out (command and output redacted)"
    elif isinstance(error, subprocess.CalledProcessError):
        message = f"subprocess exited with code {error.returncode} (command and output redacted)"
    elif isinstance(error, OSError):
        message = os.strerror(error.errno) if isinstance(error.errno, int) else "operating system operation failed"
        message += " (paths and details redacted)"
    else:
        message = "exception message redacted: may contain credentials, URLs or private payloads"
    detail = {"type": type(error).__name__, "message": message}
    if error.__cause__ is not None:
        detail["cause"] = exception_details(error.__cause__)
    return detail


def record_error(results, error, stage):
    results["status"] = "fail"
    results.setdefault("errors", []).append({"stage": stage, **exception_details(error)})


def require(test, message):
    if not test:
        raise Refusal(message)


def namespace(value):
    require(bool(re.fullmatch(r"acceptance-[a-z0-9](?:[a-z0-9-]{0,50}[a-z0-9])?", value)),
            "namespace must be a DNS name with acceptance- prefix and a unique suffix")
    return value


def image_tag(value):
    require(isinstance(value, str) and bool(re.fullmatch(r"[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}", value))
            and value.lower() not in {"latest", "dev", "main"}, "explicit non-floating test tag required")
    return value


def load_values(path):
    v = json.loads(Path(path).read_text())
    require(isinstance(v, dict), "values must be an object")
    require(bool(re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9.:/-]*", v.get("registry", "")))
            and ".." not in v["registry"] and not v["registry"].endswith("/"), "invalid registry")
    image_tag(v.get("tag"))
    nodes = v.get("excluded_nodes")
    require(isinstance(nodes, list) and nodes and len(nodes) == len(set(nodes))
            and all(isinstance(n, str) and re.fullmatch(r"[a-z0-9](?:[a-z0-9.-]{0,61}[a-z0-9])?", n) for n in nodes),
            "nonempty excluded_nodes hostname label list required")
    require(v.get("storage_class", "default") == "default", "smoke uses only the discovered default StorageClass")
    smoke_parameters(v)
    pg = v.get("postgres_image", "")
    require(bool(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.:/_-]*:[A-Za-z0-9_][A-Za-z0-9_.-]*", pg))
            and pg.rsplit(":", 1)[1] != "latest", "explicit Postgres image tag required")
    return v


def smoke_parameters(v):
    sources, targets = v.get("public_repos", []), v.get("push_targets", [])
    require(isinstance(sources, list) and all(isinstance(x, str) for x in sources) and 2 <= len(sources) <= 3 and len(set(sources)) == len(sources),
            "two or three distinct public repository URLs required")
    require(isinstance(targets, list) and all(isinstance(x, str) for x in targets) and (not targets or len(targets) == len(sources)), "one disposable push target per member or none")
    seen = set()
    for raw in sources + targets:
        require(isinstance(raw, str) and not any(c in raw for c in "\x00\r\n"), "invalid repository URL")
        u = urlsplit(raw)
        require(u.scheme == "https" and u.hostname and u.username is None and u.password is None
                and u.path and not u.query and not u.fragment, "credential-free HTTPS repository URL required")
        require("%" not in u.path and all(part not in {".", ".."} for part in u.path.split("/")), "unambiguous repository path required")
        key = (u.hostname.lower(), u.port or 443, u.path.lower().rstrip("/").removesuffix(".git"))
        require(key not in seen, "push targets must differ from all public sources and each other")
        seen.add(key)


def default_storage(kube):
    defaults = [c for c in kube.get("storageclass")["items"] if
                c["metadata"].get("annotations", {}).get("storageclass.kubernetes.io/is-default-class") == "true"
                or c["metadata"].get("annotations", {}).get("storageclass.beta.kubernetes.io/is-default-class") == "true"]
    require(len(defaults) == 1, "exactly one default StorageClass required; do not change the lab default")
    sc = defaults[0]
    require(sc.get("reclaimPolicy", "Delete") == "Delete", "disposable default class must use Delete reclaim")
    return {"name": sc["metadata"]["name"], "uid": sc["metadata"]["uid"], "provisioner": sc["provisioner"],
            "quota_enforced": False, "note": "Hostpath/local-path classes do not enforce byte or inode quotas. No exhaustion tests run; capacity admission is omitted ONLY in the test pod image."}


def atomic_json(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd, name = tempfile.mkstemp(prefix=".write-", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as f:
            json.dump(value, f, indent=2)
            f.write("\n")
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def required_affinity(pod, nodes):
    require("nodeName" not in pod, "direct node binding refuses required scheduling")
    affinity = pod.setdefault("affinity", {}).setdefault("nodeAffinity", {})
    terms = affinity.setdefault("requiredDuringSchedulingIgnoredDuringExecution", {}).setdefault("nodeSelectorTerms", [{}])
    require(terms, "empty required scheduling terms")
    for term in terms:
        term.setdefault("matchExpressions", []).extend([
            {"key": "kubernetes.io/hostname", "operator": "Exists"},
            {"key": "kubernetes.io/hostname", "operator": "NotIn", "values": list(nodes)},
        ])


def production_yaml(name):
    try:
        import yaml
    except ImportError as e:
        raise Refusal("install the distribution's python3-yaml package first") from e
    return [d for d in yaml.safe_load_all((BASE / name).read_text()) if d]


def secret_values():
    v = {k: secrets.token_hex(32) for k in (
        "POSTGRES_PASSWORD", "BLERG_CORE_REGISTER_KEY", "BLERG_CORE_INTERNAL_KEY", "BLERG_CORE_BOARD_MINT_KEY",
        "BLERG_BOARD_SERVICE_KEY", "BLERG_RUNNER_KEY", "BLERG_RUNNER_DAEMON_TOKEN")}
    for k in ("BLERG_CORE_LOCAL_KEY", "BLERG_BOARD_SECRET_KEY"):
        v[k] = base64.b64encode(secrets.token_bytes(32)).decode()
    password = v["POSTGRES_PASSWORD"]
    for key, db in [("DATABASE_URL_CORE", "blerg_core"), ("DATABASE_URL_BOARD", "blerg_board"), ("BLERG_RUNNER_DATABASE_URL", "blerg_runner")]:
        v[key] = f"postgres://blerg:{password}@postgres:5432/{db}?sslmode=disable"
    v.update(ANTHROPIC_API_KEY="", BLERG_CORE_GITHUB_TOKEN="", BLERG_RUNNER_GITHUB_TOKEN="")
    return v


def render(ns, v, run_id, credentials=None):
    namespace(ns)
    resources = []
    files = ["namespace.yaml", "configmap.yaml", "postgres-init-configmap.yaml", "postgres-statefulset.yaml",
             "core-deployment.yaml", "board-deployment.yaml", "runner-deployment.yaml", "cluster-runtime.yaml"]
    for filename in files:
        for d in production_yaml(filename):
            if d["kind"] == "Namespace":
                if resources:
                    continue  # one namespace for this isolated install and its sessions
                d["metadata"]["name"] = ns
            else:
                d["metadata"]["namespace"] = ns
            d["metadata"].setdefault("labels", {})[LABEL] = run_id
            if d["kind"] == "RoleBinding":
                for subject in d["subjects"]:
                    subject["namespace"] = ns
            if d["kind"] == "ConfigMap" and d["metadata"]["name"] == "blerg-config":
                data = d["data"]
                for key in list(data):
                    if "${" in data[key]:
                        data[key] = ""
                data.update({
                    "BLERG_CORE_PUBLIC_URL": "http://localhost:18081",
                    "BLERG_BOARD_SELF_URL": "http://localhost:18082", "BLERG_BOARD_PUBLIC_URL": "http://localhost:18082",
                    "BLERG_BOARD_AGENT_URL": "http://blerg-board:8080", "RUNNER_UI_BASE": "http://localhost:18083/sessions",
                    "BLERG_RUNNER_SELF_URL": "http://localhost:18083",
                    "BLERG_RUNNER_AGENT_IMAGE": f'{v["registry"]}/blerg-runner-agent-enabled:{v["tag"]}',
                    "BLERG_RUNNER_AGENT_NAMESPACE": ns,
                    "BLERG_RUNNER_INTERNAL_URL": f"http://blerg-runner.{ns}.svc.cluster.local:8080",
                    "BLERG_RUNNER_WORKSPACE_STORAGE_CLASS": v.get("resolved_storage_class", "default-class-resolved-at-deploy"),
                    "BLERG_RUNNER_NATIVE_HISTORY_STORAGE_CLASS": v.get("resolved_storage_class", "default-class-resolved-at-deploy"),
                    "BLERG_RUNNER_NATIVE_HISTORY_MAX_CLAIMS": "1", "BLERG_RUNNER_NATIVE_HISTORY_RETENTION_HOURS": "1",
                    "BLERG_RUNNER_MAX_SESSIONS": "1", "BLERG_RUNNER_POD_TTL_SECONDS": "3600",
                    "BLERG_RUNNER_POD_CPU_REQUEST": "100m", "BLERG_RUNNER_POD_MEM_REQUEST": "256Mi",
                    "BLERG_RUNNER_POD_CPU_LIMIT": "1", "BLERG_RUNNER_POD_MEM_LIMIT": "1Gi",
                    "BLERG_RUNNER_MCP_GW_URL": f"http://blerg-runner-mcp-gateway.{ns}.svc.cluster.local:8090",
                    "BLERG_RUNNER_MCP_GW_PUBLIC": "off", "BLERG_RUNNER_TRUSTED_PROXIES": "",
                    "BLERG_RUNNER_MODEL_CATALOG_URL": "", "BLERG_RUNNER_WEBHOOK_ALLOW_PRIVATE": "false",
                })
            if d["kind"] in {"Deployment", "StatefulSet"}:
                template = d["spec"]["template"]
                template["metadata"].setdefault("labels", {})[LABEL] = run_id
                pod = template["spec"]
                required_affinity(pod, v["excluded_nodes"])
                pod["automountServiceAccountToken"] = d["metadata"]["name"] == "blerg-runner"
                for c in pod["containers"]:
                    name = c["name"]
                    image = {"blerg-core": "blerg-core", "blerg-board": "blerg-board", "blerg-runner": "blerg-runner-enabled"}.get(name)
                    c["image"] = f'{v["registry"]}/{image}:{v["tag"]}' if image else v["postgres_image"]
                    c["imagePullPolicy"] = "IfNotPresent"
                    c["resources"] = {"requests": {"cpu": "100m", "memory": "128Mi"}, "limits": {"cpu": "1", "memory": "768Mi"}}
                    if name == "blerg-core":
                        origins = ["http://localhost:18081", "http://localhost:18082", "http://localhost:18083"]
                        replacements = {"BLERG_CORE_ALLOWED_RETURN_ORIGINS": ",".join(origins),
                                        "BLERG_CORE_ORIGIN_AUDIENCES": ",".join(o+"="+a for o,a in zip(origins,["blerg-core","blerg-board","blerg-runner"])),
                                        "BLERG_CORE_BEHIND_PROXY": "false"}
                        # The setup handler still requires a loopback TCP peer,
                        # localhost Host and no forwarded headers. Only the
                        # controller's loopback port-forward bootstraps this install.
                        c["env"].extend([{"name": "BLERG_SETUP_ALLOW_LOCAL", "value": "true"},
                                         {"name": "BLERG_SETUP_PUBLISHED_ADDR", "value": "localhost"}])
                        for entry in c["env"]:
                            if entry["name"] in replacements:
                                entry["value"] = replacements[entry["name"]]
                    if name == "blerg-runner":
                        c.setdefault("env", []).append({"name": "BLERG_RUNNER_POD_EXCLUDE_NODES", "value": ",".join(v["excluded_nodes"])})
                if d["kind"] == "StatefulSet":
                    for claim in d["spec"]["volumeClaimTemplates"]:
                        if v.get("resolved_storage_class"):
                            claim["spec"]["storageClassName"] = v["resolved_storage_class"]
                        else:
                            claim["spec"].pop("storageClassName", None)
                        claim["spec"]["resources"]["requests"]["storage"] = "2Gi"
            resources.append(d)
    resources += [
        {"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": {"name": "blerg-runner-data", "namespace": ns},
         "spec": {"accessModes": ["ReadWriteOnce"], "storageClassName": v.get("resolved_storage_class", "default-class-resolved-at-deploy"), "resources": {"requests": {"storage": "1Gi"}}}},
        {"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "blerg-secrets", "namespace": ns}, "type": "Opaque",
         "stringData": credentials or {k: "RENDER-ONLY-NOT-A-CREDENTIAL" for k in secret_values()}},
        {"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "blerg-runner-agent", "namespace": ns}, "type": "Opaque", "stringData": {}},
        {"apiVersion": "v1", "kind": "LimitRange", "metadata": {"name": "acceptance-limits", "namespace": ns}, "spec": {"limits": [
            {"type": "Container", "defaultRequest": {"cpu": "100m", "memory": "128Mi", "ephemeral-storage": "64Mi"}, "default": {"cpu": "1", "memory": "1Gi", "ephemeral-storage": "1Gi"},
             "max": {"cpu": "2", "memory": "2Gi", "ephemeral-storage": "2Gi"}}]}},
        {"apiVersion": "v1", "kind": "ResourceQuota", "metadata": {"name": "acceptance-budget", "namespace": ns}, "spec": {"hard": {
            "pods": "12", "requests.cpu": "4", "requests.memory": "6Gi", "limits.cpu": "12", "limits.memory": "12Gi",
            "requests.storage": "20Gi", "persistentvolumeclaims": "5", "count/jobs.batch": "16", "count/secrets": "32"}}},
        {"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": {"name": "private-install", "namespace": ns},
         "spec": {"podSelector": {}, "policyTypes": ["Ingress"], "ingress": [{"from": [{"podSelector": {}}]}]}},
    ]
    for d in resources:
        if d["kind"] == "PersistentVolumeClaim" and not v.get("resolved_storage_class"):
            d["spec"].pop("storageClassName", None)
        d["metadata"].setdefault("labels", {})[LABEL] = run_id
    encoded = json.dumps(resources)
    require("${" not in encoded and '"namespace": "blerg"' not in encoded, "unresolved or production-scoped manifest")
    require(all(d["kind"] not in {"Ingress", "ClusterRole", "ClusterRoleBinding", "StorageClass", "PersistentVolume"} for d in resources), "cluster-scoped or exposed install object")
    return {"apiVersion": "v1", "kind": "List", "items": resources}


class Kube:
    def __init__(self, context, ns):
        require(context and not context.startswith("-"), "explicit --context required")
        self.context, self.ns = context, namespace(ns)

    def call(self, *args, data=None, check=True):
        # Do not log commands, stdin, raw errors or Secret bodies.
        command = ["kubectl", "--context", self.context, "--namespace", self.ns, "--request-timeout=30s", *args]
        p = subprocess.run(command, input=json.dumps(data) if data is not None else None, capture_output=True, text=True, timeout=1080)
        require(not check or p.returncode == 0, "kubectl operation failed (details intentionally withheld; inspect only this namespace)")
        return p

    def get(self, kind, name=None):
        return json.loads(self.call("get", kind, *([name] if name else []), "-o", "json").stdout)

    def optional(self, kind, name):
        # Ignore only NotFound, never connection/auth errors.
        raw = self.call("get", kind, name, "--ignore-not-found", "-o", "json").stdout
        return json.loads(raw) if raw.strip() else None

    def owned(self, state):
        obj = self.get("namespace", self.ns)
        require(obj["metadata"]["uid"] == state["namespace_uid"] and obj["metadata"].get("labels", {}).get(LABEL) == state["run_id"],
                "namespace identity/ownership changed; refusing")
        return obj

    def delete_uid(self, kind, name, uid):
        require(kind in {"namespaces", "persistentvolumes"} and re.fullmatch(r"[a-z0-9][a-z0-9.-]*", name), "invalid deletion target")
        if kind == "namespaces":
            namespace(name)
        payload = {"apiVersion": "v1", "kind": "DeleteOptions", "propagationPolicy": "Foreground", "preconditions": {"uid": uid}}
        fd, filename = tempfile.mkstemp(prefix="acceptance-delete-")
        try:
            with os.fdopen(fd, "w") as f:
                json.dump(payload, f)
            self.call("delete", "--raw", f"/api/v1/{kind}/{name}", "-f", filename)
        finally:
            os.unlink(filename)


def state_path(args):
    return Path(args.state).resolve() / "namespace.json"


def load_state(args):
    s = json.loads(state_path(args).read_text())
    require(s["namespace"] == namespace(args.namespace) and s["context"] == args.context, "state namespace/context mismatch")
    require(re.fullmatch(r"[a-f0-9]{32}", s["run_id"]) is not None and s.get("namespace_uid"), "invalid ownership journal")
    return s


def placement(kube, v):
    nodes = kube.get("nodes")["items"]
    labels = {n["metadata"]["name"]: n["metadata"].get("labels", {}).get("kubernetes.io/hostname") for n in nodes}
    require(any(host and host not in v["excluded_nodes"] for host in labels.values()), "no eligible labelled node")
    for pod in kube.get("pods")["items"]:
        spec = pod["spec"]
        terms = spec.get("affinity", {}).get("nodeAffinity", {}).get("requiredDuringSchedulingIgnoredDuringExecution", {}).get("nodeSelectorTerms", [])
        require(terms and all(any(e.get("key") == "kubernetes.io/hostname" and e.get("operator") == "NotIn"
                                 and set(v["excluded_nodes"]) <= set(e.get("values", [])) for e in t.get("matchExpressions", [])) for t in terms),
                "pod lacks required node exclusions")
        assigned = spec.get("nodeName")
        require(not assigned or (labels.get(assigned) and labels[assigned] not in v["excluded_nodes"]), "pod scheduled on excluded/unidentified node")


def metadata_only(obj):
    """Never persist Secret data, env literals, prompts, URLs, or raw pod logs."""
    m = obj["metadata"]
    out = {"kind": obj.get("kind"), "name": m["name"], "uid": m["uid"], "namespace": m.get("namespace"), "owners": m.get("ownerReferences", [])}
    status = obj.get("status", {})
    out["status"] = {k: status[k] for k in ("phase", "readyReplicas", "succeeded", "failed") if k in status}
    spec = obj.get("spec", {})
    if "nodeName" in spec:
        out["node"] = spec["nodeName"]
        out["images"] = [{"container": c["name"], "image_id": c.get("imageID", "")} for c in status.get("containerStatuses", [])]
    return out


def collect(kube, state, directory):
    kube.owned(state)
    output = []
    for kind in ("pods", "jobs", "persistentvolumeclaims", "secrets", "deployments", "statefulsets"):
        output.extend(metadata_only(o) for o in kube.get(kind)["items"])
    atomic_json(Path(directory) / "inventory.json", output)


def pv_inventory(kube, state):
    kube.owned(state)
    output = []
    for pvc in kube.get("persistentvolumeclaims")["items"]:
        name = pvc.get("spec", {}).get("volumeName")
        if not name:
            continue
        pv = kube.get("persistentvolume", name)
        claim = pv.get("spec", {}).get("claimRef", {})
        require(claim.get("namespace") == state["namespace"] and claim.get("uid") == pvc["metadata"]["uid"], "PV claim identity differs")
        output.append({"name": name, "uid": pv["metadata"]["uid"], "claim_uid": claim["uid"]})
    return output


def deploy(kube, args, values, state):
    doc = render(kube.ns, values, state["run_id"], secret_values())
    ns = next(d for d in doc["items"] if d["kind"] == "Namespace")
    created = json.loads(kube.call("create", "-f", "-", "-o", "json", data=ns).stdout)
    state["namespace_uid"] = created["metadata"]["uid"]
    # Journal before applying anything else; partial deployments remain teardown-able.
    atomic_json(state_path(args), state)
    kube.call("apply", "-f", "-", data={"apiVersion": "v1", "kind": "List", "items": [d for d in doc["items"] if d["kind"] != "Namespace"]})
    for name in ("statefulset/postgres", "deployment/blerg-core", "deployment/blerg-board", "deployment/blerg-runner"):
        kube.call("rollout", "status", name, "--timeout=600s")
    placement(kube, values)


def campaign_fingerprint():
    sources = [Path(__file__), HERE / "cases.json", HERE / "Dockerfile", HERE / "test-entry.py", HERE / "scripted-engine.py", HERE / "smoke-build.py",
               HERE.parents[2] / "runner/internal/server/acceptance_smoke_integration_test.go"]
    digest = hashlib.sha256()
    for file in sources:
        digest.update(file.name.encode()+b"\0"+file.read_bytes())
    return digest.hexdigest()


def deployment_fingerprint(values):
    return hashlib.sha256(json.dumps(values, sort_keys=True).encode()).hexdigest()


def cases():
    return json.loads((HERE / "cases.json").read_text())


def serviceaccount_can_i(kube, *action):
    sa = f'system:serviceaccount:{kube.ns}:blerg-runner-agent-launcher'
    answer = kube.call("auth", "can-i", *action, "--as", sa,
                       "--as-group=system:serviceaccounts", "--as-group=system:serviceaccounts:"+kube.ns,
                       "--as-group=system:authenticated", check=False)
    require((answer.returncode, answer.stdout.strip()) in {(0, "yes"), (1, "no")},
            "service account authorization probe failed or returned an invalid answer")
    return answer.returncode == 0


def effective_exec_denied(kube):
    if not serviceaccount_can_i(kube, "create", "pods", "--subresource=exec"):
        return {"status": "pass", "message": "effective pods/exec denial established"}
    # Neither action is granted by the unchanged production Role. Both are
    # authorization questions only: never read Secret contents or create a role.
    secrets_allowed = serviceaccount_can_i(kube, "get", "secrets", "--namespace=kube-system")
    roles_allowed = serviceaccount_can_i(kube, "create", "clusterroles.rbac.authorization.k8s.io")
    require(secrets_allowed and roles_allowed, "effective pods/exec denial not established on an RBAC cluster")
    return {"status": "pass-with-limitation", "message": RBAC_LIMITATION}


LAUNCH_STAGES = {"selection", "manifest", "authorization", "credentials", "storage", "writer_fence", "runtime_binding",
                 "plugins_resolve", "launch_admission", "runtime_token", "launch_credentials", "pod_manifest", "secrets", "queued", "schedule", "image",
                 "bootstrap", "connect", "negotiation", "clone", "workspace_prepare", "plugins", "engine", "ready", "shell_open"}


class SmokeFailure(Refusal):
    def __init__(self, diagnostics):
        failed = next((d["stage"] for d in diagnostics if d["outcome"] == "failed"), None)
        active = next((d["stage"] for d in diagnostics if d["outcome"] == "active"), None)
        super().__init__("real-pod smoke failed at launch stage " + (failed or active or "unknown"))
        self.diagnostics = diagnostics


# Kubernetes Status reasons and local submission guards are enum-only. Bodies,
# messages and validation details never enter the campaign receipt.
JOB_SUBMISSION_REASONS = {"AlreadyExists", "Forbidden", "Invalid", "Unauthorized", "BadRequest", "NotFound", "Conflict",
                          "TooManyRequests", "ServiceUnavailable", "InternalError", "Timeout", "ServerTimeout",
                          "UnknownAPIError", "SchedulingPolicyRefused", "TransportError"}


SHELL_REFUSAL_REASONS = {"session_not_ready", "binding_refused", "policy_unavailable", "policy_disabled",
                         "authority_refused", "runtime_refused", "unavailable"}


def launch_diagnostics(raw):
    """Project an allowlisted receipt; never copy raw logs or arbitrary text."""
    marker = "ACCEPTANCE_LAUNCH_DIAGNOSTICS "
    found = []
    for line in raw.splitlines():
        if marker not in line:
            continue
        try:
            records = json.loads(line.split(marker, 1)[1])
        except (ValueError, TypeError):
            continue
        if not isinstance(records, list) or len(records) > 256:
            continue
        clean = []
        for record in records:
            if not isinstance(record, dict) or record.get("stage") not in LAUNCH_STAGES:
                continue
            if record.get("outcome") not in {"pending", "active", "done", "failed", "warning"}:
                continue
            if record.get("reason") not in {"not_started", "in_progress", "completed", "stage_refused", "completed_with_warning", "pod_inventory_refused", "capabilities_mismatch", "init_failed", "init_configuration_refused", "runtime_startup_refused", "stage_deadline_exceeded"} | JOB_SUBMISSION_REASONS | SHELL_REFUSAL_REASONS:
                continue
            item = {k: record[k] for k in ("stage", "outcome", "reason")}
            for key in ("started_at", "finished_at"):
                value = record.get(key, "")
                if isinstance(value, str) and re.fullmatch(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,9})?Z", value):
                    item[key] = value
            clean.append(item)
        found = clean
    return found


def wait_smoke_job(kube, name, identity, run_id, timeout=840):
    deadline = time.monotonic() + timeout
    while True:
        observed = kube.get("job", name)
        require(observed["metadata"]["uid"] == identity["uid"] and observed["metadata"].get("labels", {}).get(LABEL) == run_id, "smoke Job identity changed")
        status = observed.get("status", {})
        conditions = status.get("conditions", [])
        if status.get("failed", 0) or status.get("succeeded", 0) or any(c.get("type") in {"Complete", "Failed", "FailureTarget"} and c.get("status") == "True" for c in conditions):
            return
        if time.monotonic() >= deadline:
            return  # Collect the bounded receipt even on timeout.
        time.sleep(1)


def smoke_receipt(raw):
    require(len(raw) <= 2*1024*1024, "smoke output too large")
    marker = "ACCEPTANCE_SMOKE_PASS clones=distinct explorer=list,search,status shell=open,close sync=fixture-push stop=jobs,secrets,tokens quotas=NOT_ENFORCED"
    require(raw.count(marker) == 1 and "--- PASS: TestAcceptanceClusterSmoke " in raw
            and "--- SKIP:" not in raw and "--- FAIL:" not in raw, "real-pod smoke did not complete every assertion")
    return {"public_clones_distinct": True, "explorer": ["list", "search", "status"], "shell": ["open", "close"],
            "sync": "committed wip/session branch received and verified in namespace-local bare fixture",
            "stop": ["Job absent", "Pods absent", "Secret absent", "tokens revoked"], "quota_enforced": False}


def run_smoke(kube, values, state, args):
    name = "acceptance-smoke-" + uuid.uuid4().hex[:12]
    labels = {LABEL: state["run_id"], "acceptance-smoke": state["run_id"]}
    service = {"apiVersion": "v1", "kind": "Service", "metadata": {"name": "acceptance-smoke", "namespace": kube.ns, "labels": labels},
               "spec": {"type": "ClusterIP", "selector": {"acceptance-smoke": state["run_id"]},
                        "ports": [{"name": "api", "port": 8080, "targetPort": 8080}, {"name": "git", "port": 443, "targetPort": 8443}]}}
    env = {"P6_ACCEPTANCE_SMOKE": "1", "BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS": "false", "P6_PUBLIC_REPOS": json.dumps(values["public_repos"]),
           "P6_PUSH_TARGETS": json.dumps(values.get("push_targets", [])), "BLERG_RUNNER_AGENT_NAMESPACE": kube.ns,
           "BLERG_RUNNER_POD_EXCLUDE_NODES": ",".join(values["excluded_nodes"]),
           "BLERG_RUNNER_AGENT_IMAGE": f'{values["registry"]}/blerg-runner-agent-enabled:{values["tag"]}',
           "BLERG_RUNNER_INTERNAL_URL": "http://acceptance-smoke:8080", "BLERG_RUNNER_AGENT_GIT_BASE": "https://acceptance-smoke", "BLERG_RUNNER_NATIVE_HISTORY": "false",
           "BLERG_RUNNER_WORKSPACE_STORAGE_CLASS": state["storage"]["name"], "BLERG_RUNNER_MAX_SESSIONS": "1",
           "BLERG_RUNNER_POD_CPU_REQUEST": "100m", "BLERG_RUNNER_POD_CPU_LIMIT": "1",
           "BLERG_RUNNER_POD_MEM_REQUEST": "256Mi", "BLERG_RUNNER_POD_MEM_LIMIT": "512Mi",
           "BLERG_RUNNER_POD_TERMINATION_GRACE_SECONDS": "90"}
    container = {"name": "smoke", "image": f'{values["registry"]}/blerg-acceptance-tests:{values["tag"]}',
                 "imagePullPolicy": "IfNotPresent", "securityContext": {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}},
                 "resources": {"requests": {"cpu": "100m", "memory": "256Mi", "ephemeral-storage": "128Mi"},
                               "limits": {"cpu": "1", "memory": "768Mi", "ephemeral-storage": "2Gi"}},
                 "env": [{"name": k, "value": v} for k, v in env.items()] + [
                     {"name": "TEST_DATABASE_URL", "valueFrom": {"secretKeyRef": {"name": "blerg-secrets", "key": "BLERG_RUNNER_DATABASE_URL"}}}]}
    pod = {"restartPolicy": "Never", "serviceAccountName": "blerg-runner-agent-launcher", "automountServiceAccountToken": True,
           "securityContext": {"runAsNonRoot": True, "runAsUser": 1000, "seccompProfile": {"type": "RuntimeDefault"}}, "containers": [container]}
    required_affinity(pod, values["excluded_nodes"])
    job = {"apiVersion": "batch/v1", "kind": "Job", "metadata": {"name": name, "namespace": kube.ns, "labels": labels},
           "spec": {"backoffLimit": 0, "activeDeadlineSeconds": 900,
                    "template": {"metadata": {"labels": labels}, "spec": pod}}}
    # A failed smoke remains for collection/UID-checked namespace teardown. Never
    # adopt/reuse a Service from an earlier run or automatically retry a session.
    kube.call("create", "-f", "-", data=service)
    created = json.loads(kube.call("create", "-f", "-", "-o", "json", data=job).stdout)
    state["smoke_job"] = {"name": name, "uid": created["metadata"]["uid"]}
    atomic_json(state_path(args), state)
    wait_smoke_job(kube, name, state["smoke_job"], state["run_id"])
    observed = kube.get("job", name)
    require(observed["metadata"]["uid"] == state["smoke_job"]["uid"] and observed["metadata"].get("labels", {}).get(LABEL) == state["run_id"], "smoke Job identity changed")
    # Parse bounded test output in memory. Do not persist raw logs/URLs/tokens.
    raw = kube.call("logs", "job/"+name, "--container=smoke", "--limit-bytes=2097152").stdout
    diagnostics = launch_diagnostics(raw)
    try:
        evidence = smoke_receipt(raw)
    except Refusal as error:
        raise SmokeFailure(diagnostics) from error
    evidence["launch_stages"] = diagnostics
    evidence.update(job=state["smoke_job"], members=len(values["public_repos"]),
                    target_mode="fixture-and-disposable-relay" if values.get("push_targets") else "namespace-local-bare-fixture",
                    disposable_targets_verified=len(values.get("push_targets", [])))
    return evidence


def execute(args):
    ns = namespace(args.namespace)
    if args.dry_run:
        json.dump(render(ns, load_values(args.values), "render-only"), sys.stdout, indent=2)
        print()
        return 0
    require(args.context and args.state, "--context and private --state required")
    root = Path(args.state).resolve()
    require(not root.exists() or not root.is_symlink(), "state directory cannot be a symlink")
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    require(root.stat().st_mode & 0o077 == 0, "state directory must be private (0700)")
    results = {"schema": SCHEMA, "campaign_sha256": campaign_fingerprint(), "namespace": ns, "status": "not-run",
               "scope": "0.5.0 happy-path smoke only", "deferred": {"card": 180, "release": "0.5.1", "cases": [c["id"] for c in cases()]},
               "limitations": ["Default hostpath/local-path storage has NO enforced byte/inode quota; no exhaustion tests.",
                               "Test image omits capacity admission; production capacity checks remain enabled.",
                               "Public inputs are anonymously mirrored; only namespace-local fixture push authority is minted.",
                               "Core identity and engine are deterministic fixtures; no model/provider credentials or Core/UI acceptance."],
               "cases": [{"id": "cluster-smoke", "status": "not-run"}]}
    out = root / "results.json"
    if out.exists():
        os.replace(out, root / ("results-"+uuid.uuid4().hex+".json"))
    atomic_json(out, results)
    row = results["cases"][0]
    start = time.monotonic()
    kube, state = None, None
    stage = "configuration"
    try:
        values = load_values(args.values)
        kube = Kube(args.context, ns)
        stage = "resume" if args.resume else "deploy"
        if args.resume:
            state = load_state(args)
            kube.owned(state)
            require(state.get("campaign_sha256") == campaign_fingerprint(), "campaign code/registry changed after deployment")
            require(state["values_sha256"] == deployment_fingerprint(values), "deployment values changed")
            sc = default_storage(kube)
            require(sc["uid"] == state["storage"]["uid"], "default StorageClass changed since deployment")
        else:
            require(not state_path(args).exists(), "state already exists; use --resume")
            state = {"schema": SCHEMA, "namespace": ns, "context": args.context, "run_id": uuid.uuid4().hex,
                     "values_sha256": deployment_fingerprint(values), "campaign_sha256": campaign_fingerprint(), "storage": default_storage(kube)}
            nodes = kube.get("nodes")["items"]
            labels = {n["metadata"].get("labels", {}).get("kubernetes.io/hostname") for n in nodes}
            require(set(values["excluded_nodes"]) <= labels, "excluded hostname labels do not match cluster nodes")
            require(any(label and label not in values["excluded_nodes"] for label in labels), "no eligible node")
            deployed = dict(values, resolved_storage_class=state["storage"]["name"])
            deploy(kube, args, deployed, state)
        results.update(run_id=state["run_id"], namespace_uid=state["namespace_uid"], storage=state["storage"])
        if args.deploy_only:
            results["status"] = "deployed-not-accepted"
        else:
            stage = "before-smoke"
            kube.owned(state)
            placement(kube, values)
            stage = "authorization-before-smoke"
            check = effective_exec_denied(kube)
            row["checks"] = {"exec_denied": check}
            limited = check["status"] == "pass-with-limitation"
            if limited:
                results["limitations"].append(RBAC_LIMITATION)
            row["status"] = "running"
            atomic_json(out, results)
            stage = "smoke"
            row["evidence"] = run_smoke(kube, values, state, args)
            stage = "after-smoke"
            placement(kube, values)
            stage = "authorization-after-smoke"
            after = effective_exec_denied(kube)
            # Once observed, a limitation cannot be erased by a later denial.
            if after["status"] == "pass-with-limitation":
                row["checks"]["exec_denied"] = after
                if not limited:
                    results["limitations"].append(RBAC_LIMITATION)
                limited = True
            row["evidence"]["exec_denied"] = RBAC_LIMITATION if limited else True
            row["status"] = results["status"] = "pass-rbac-not-enforced" if limited else "pass"
    except Exception as error:
        record_error(results, error, stage)
        row["status"] = "fail"
        if isinstance(error, SmokeFailure):
            row["evidence"] = {"launch_stages": error.diagnostics}
        if stage in {"authorization-before-smoke", "authorization-after-smoke"}:
            row.setdefault("checks", {})["exec_denied"] = {"status": "fail", **exception_details(error)}
    finally:
        row["duration_seconds"] = round(time.monotonic()-start, 3)
        atomic_json(out, results)
    if kube is not None and state is not None and state.get("namespace_uid"):
        try:
            collect(kube, state, root)
        except Exception as error:
            record_error(results, error, "collection")
            row["status"] = "fail"
            atomic_json(out, results)
    if results["status"] in {"pass", "pass-rbac-not-enforced", "deployed-not-accepted"}:
        return 0
    return 2 if stage in {"configuration", "resume", "deploy"} else 1


def teardown(args):
    ns = namespace(args.namespace)
    state = load_state(args)
    if args.dry_run:
        print(json.dumps({"namespace": ns, "required_namespace_uid": state["namespace_uid"], "actions": ["verify ownership and bound claim/PV UIDs", "foreground delete namespace with UID precondition", "wait for namespace deletion", "verify retained PV claim UIDs and delete only those PVs with UID preconditions"], "contacts_cluster": False}, indent=2))
        return 0
    kube = Kube(args.context, ns)
    obj = kube.optional("namespace", ns)
    if obj is not None:
        kube.owned(state)
        saved = {p["name"]: p for p in state.get("pvs", [])}
        for bound in pv_inventory(kube, state):
            require(bound["name"] not in saved or saved[bound["name"]] == bound, "PV identity changed during teardown retry")
            saved[bound["name"]] = bound
        state["pvs"] = list(saved.values())
        atomic_json(state_path(args), state)  # checkpoint before delete, supports retry
        kube.delete_uid("namespaces", ns, state["namespace_uid"])
        kube.call("wait", "--for=delete", "namespace/" + ns, "--timeout=600s")
    else:
        require("pvs" in state, "namespace absent without a teardown PV journal; refusing PV guesses")
    # Never strip finalizers or force-delete. Re-read after namespace removal.
    require(kube.optional("namespace", ns) is None, "namespace recreated; refusing")
    for recorded in state["pvs"]:
        pv = kube.optional("persistentvolume", recorded["name"])
        if pv is None:
            continue
        claim = pv.get("spec", {}).get("claimRef", {})
        require(pv["metadata"]["uid"] == recorded["uid"] and claim.get("namespace") == ns and claim.get("uid") == recorded["claim_uid"], "PV replaced or rebound; refusing")
        kube.delete_uid("persistentvolumes", recorded["name"], recorded["uid"])
        kube.call("wait", "--for=delete", "persistentvolume/" + recorded["name"], "--timeout=600s")
    atomic_json(Path(args.state) / "teardown.json", {"namespace": ns, "namespace_uid": state["namespace_uid"], "status": "deleted", "pvs": state["pvs"]})
    print("Deleted only the owned namespace and UID-verified associated PVs; check CSI backend reclamation.")
    return 0


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["run", "collect", "teardown"])
    parser.add_argument("--namespace", required=True)
    parser.add_argument("--context")
    parser.add_argument("--state")
    parser.add_argument("--values", default=str(HERE / "values.example.json"))
    parser.add_argument("--dry-run", nargs="?", const="server-free", choices=["server-free"])
    parser.add_argument("--deploy-only", action="store_true")
    parser.add_argument("--resume", action="store_true")
    args = parser.parse_args(argv)
    try:
        namespace(args.namespace)
        if args.action == "run":
            return execute(args)
        require(args.state and args.context, "explicit --state and --context required")
        if args.action == "teardown":
            return teardown(args)
        require(not args.dry_run, "collect is a cluster read; use run --dry-run for rendering")
        state = load_state(args)
        collect(Kube(args.context, args.namespace), state, args.state)
        return 0
    except Exception as error:
        detail = exception_details(error)
        print(f"REFUSED/FAILED: {detail['type']}: {detail['message']}. No automatic teardown.", file=sys.stderr)
        # Rendering stays entirely state-free. Invalid/unwritable state cannot
        # receive a results file; report that recording failure safely on stderr.
        if args.state and not args.dry_run:
            try:
                namespace(args.namespace)
                root = Path(args.state).resolve()
                require(not Path(args.state).is_symlink(), "state directory cannot be a symlink")
                require(root.is_dir() and root.stat().st_mode & 0o077 == 0, "state directory must be private (0700)")
                out = root / "results.json"
                results = {"schema": SCHEMA, "namespace": args.namespace}
                if out.exists():
                    try:
                        results = json.loads(out.read_text())
                        require(isinstance(results, dict), "results must be an object")
                    except Exception as read_error:
                        results = {"schema": SCHEMA, "namespace": args.namespace}
                        record_error(results, read_error, "read-results")
                record_error(results, error, args.action)
                atomic_json(out, results)
            except Exception as recording_error:
                detail = exception_details(recording_error)
                print(f"Results unavailable: {detail['type']}: {detail['message']}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
