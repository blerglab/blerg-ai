#!/usr/bin/env python3
"""Prepare/run an exclusively owned namespace. Dry run never invokes kubectl.

Case adapters are reviewed executable test code, not declarative assertions.
Missing adapters block the campaign BEFORE deployment. No default context,
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
import signal
import subprocess
import sys
import tempfile
import time
import uuid

HERE = Path(__file__).resolve().parent
BASE = HERE.parent
LABEL = "blerg.acceptance/run"
SCHEMA = 1


class Refusal(Exception):
    pass


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
    for key in ("workspace_storage_class", "data_storage_class"):
        require(bool(re.fullmatch(r"[a-z0-9][a-z0-9.-]{0,252}", v.get(key, ""))), "explicit storage classes required")
    pg = v.get("postgres_image", "")
    require(bool(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.:/_-]*:[A-Za-z0-9_][A-Za-z0-9_.-]*", pg))
            and pg.rsplit(":", 1)[1] != "latest", "explicit Postgres image tag required")
    return v


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
                    "BLERG_RUNNER_WORKSPACE_STORAGE_CLASS": v["workspace_storage_class"],
                    "BLERG_RUNNER_NATIVE_HISTORY_STORAGE_CLASS": v["data_storage_class"],
                    "BLERG_RUNNER_NATIVE_HISTORY_MAX_CLAIMS": "8", "BLERG_RUNNER_NATIVE_HISTORY_RETENTION_HOURS": "1",
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
                        claim["spec"]["storageClassName"] = v["data_storage_class"]
                        claim["spec"]["resources"]["requests"]["storage"] = "2Gi"
            resources.append(d)
    resources += [
        {"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": {"name": "blerg-runner-data", "namespace": ns},
         "spec": {"accessModes": ["ReadWriteOnce"], "storageClassName": v["data_storage_class"], "resources": {"requests": {"storage": "1Gi"}}}},
        {"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "blerg-secrets", "namespace": ns}, "type": "Opaque",
         "stringData": credentials or {k: "RENDER-ONLY-NOT-A-CREDENTIAL" for k in secret_values()}},
        {"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "blerg-runner-agent", "namespace": ns}, "type": "Opaque", "stringData": {}},
        {"apiVersion": "v1", "kind": "LimitRange", "metadata": {"name": "acceptance-limits", "namespace": ns}, "spec": {"limits": [
            {"type": "Container", "defaultRequest": {"cpu": "100m", "memory": "128Mi", "ephemeral-storage": "64Mi"}, "default": {"cpu": "1", "memory": "1Gi", "ephemeral-storage": "1Gi"},
             "max": {"cpu": "2", "memory": "2Gi", "ephemeral-storage": "2Gi"}}]}},
        {"apiVersion": "v1", "kind": "ResourceQuota", "metadata": {"name": "acceptance-budget", "namespace": ns}, "spec": {"hard": {
            "pods": "12", "requests.cpu": "4", "requests.memory": "6Gi", "limits.cpu": "12", "limits.memory": "12Gi",
            "requests.storage": "64Gi", "persistentvolumeclaims": "12", "count/jobs.batch": "16", "count/secrets": "32"}}},
        {"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": {"name": "private-install", "namespace": ns},
         "spec": {"podSelector": {}, "policyTypes": ["Ingress"], "ingress": [{"from": [{"podSelector": {}}]}]}},
    ]
    for d in resources:
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
        p = subprocess.run(command, input=json.dumps(data) if data is not None else None, capture_output=True, text=True, timeout=720)
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
    return hashlib.sha256(Path(__file__).read_bytes() + (HERE / "cases.json").read_bytes()).hexdigest()


def deployment_fingerprint(values):
    deployed = {k: v for k, v in values.items() if k not in {"case_drivers", "fixtures_file"}}
    return hashlib.sha256(json.dumps(deployed, sort_keys=True).encode()).hexdigest()


def cases():
    return json.loads((HERE / "cases.json").read_text())


def driver_gaps(values):
    adapters = values.get("case_drivers", {})
    missing = []
    for case in cases():
        if case["kind"] != "driver":
            continue
        entry = adapters.get(case["id"], {})
        argv = entry.get("argv")
        if not (isinstance(argv, list) and argv and all(isinstance(x, str) and x for x in argv)
                and Path(argv[0]).is_absolute() and Path(argv[0]).is_file() and os.access(argv[0], os.X_OK)
                and isinstance(entry.get("timeout_seconds"), int) and 1 <= entry["timeout_seconds"] <= 7200):
            missing.append(case["id"])
    return missing


def validate_evidence(case, path, state):
    require(Path(path).stat().st_size <= 1024 * 1024, "case report too large")
    result = json.loads(Path(path).read_text())
    require(result.get("case") == case["id"] and result.get("namespace_uid") == state["namespace_uid"]
            and result.get("run_id") == state["run_id"], "case report not bound to this install")
    obs = result.get("observations", {})
    require(all(isinstance(obs.get(k), dict) and obs[k].get("passed") is True and obs[k].get("evidence") for k in case["observations"]),
            "missing or failed required observations")
    for key in case["observations"]:
        refs = obs[key]["evidence"]
        require(isinstance(refs, list) and refs, "hashed evidence artifacts required")
        for ref in refs:
            require(isinstance(ref, dict) and isinstance(ref.get("path"), str), "invalid evidence reference")
            relative = Path(ref["path"])
            require(not relative.is_absolute() and ".." not in relative.parts, "evidence must stay inside case directory")
            artifact = Path(path).parent / relative
            require(artifact.is_file() and not artifact.is_symlink() and artifact.resolve().is_relative_to(Path(path).parent.resolve())
                    and 0 < artifact.stat().st_size <= 8*1024*1024, "missing/unsafe evidence artifact")
            require(hashlib.sha256(artifact.read_bytes()).hexdigest() == ref.get("sha256"), "evidence digest differs")
    if case["id"].startswith("measure-"):
        count, profile = case["id"].split("-", 2)[1:]
        measurement = result.get("measurement", {})
        require(measurement.get("members") == int(count) and measurement.get("profile") == profile, "wrong measurement profile")
        require(all(isinstance(measurement.get(k), str) and measurement[k] for k in
                    ("image_id", "commit", "storage_class", "csi_driver", "node")), "target identity missing")
        samples = measurement.get("samples", [])
        keys = ("prepare_ms", "snapshot_ms", "sync_ms", "stop_ms", "control_ms", "bytes", "inodes", "cpu_seconds", "peak_rss_bytes")
        require(len(samples) >= 3 and all(isinstance(s.get("cold"), bool) for s in samples) and {s["cold"] for s in samples} == {True, False}, "cold/warm repetitions required")
        require(all(all(isinstance(s.get(k), (int, float)) and not isinstance(s[k], bool) and 0 <= s[k] < 2**63 for k in keys) for s in samples),
                "complete finite target measurements required")
        measurement["latency_percentiles"] = {}
        for key in ("prepare_ms", "snapshot_ms", "sync_ms", "stop_ms", "control_ms"):
            ordered = sorted(s[key] for s in samples)
            measurement["latency_percentiles"][key] = {str(p): ordered[max(0, (len(ordered)*p+99)//100-1)] for p in (50, 95, 99)}
    require(result.get("status") == "pass", "case did not pass")
    return result


def validate_fixtures(values, state):
    file = Path(values["fixtures_file"])
    require(file.is_file() and file.stat().st_mode & 0o077 == 0 and file.stat().st_size <= 1024*1024, "private bounded fixture file required")
    fixture = json.loads(file.read_text())
    install = fixture.get("install", {})
    require(install.get("namespace") == state["namespace"] and install.get("namespace_uid") == state["namespace_uid"]
            and install.get("run_id") == state["run_id"], "fixture identity belongs to another install")
    urls = install.get("urls", {})
    for component, port in (("core", 18081), ("board", 18082), ("runner", 18083)):
        require(urls.get(component) in {f"http://localhost:{port}", f"http://blerg-{component}.{state['namespace']}.svc.cluster.local:8080"},
                "fixture install endpoint is outside the isolated install")


def run_driver(case, values, args, state):
    entry = values["case_drivers"][case["id"]]
    report = Path(args.state).resolve() / (case["id"] + ".json")
    if report.exists():
        report.unlink()  # an old receipt can never satisfy a new invocation
    env = {"PATH": "/usr/local/bin:/usr/bin:/bin", "LANG": "C.UTF-8", "HOME": str(Path(args.state).resolve())}
    env.update(ACCEPTANCE_NAMESPACE=state["namespace"], ACCEPTANCE_NAMESPACE_UID=state["namespace_uid"],
               ACCEPTANCE_CONTEXT=state["context"], ACCEPTANCE_RUN_ID=state["run_id"], ACCEPTANCE_REPORT=str(report),
               ACCEPTANCE_FIXTURES_FILE=values["fixtures_file"], ACCEPTANCE_CASE=case["id"], ACCEPTANCE_CASES_FILE=str(HERE / "cases.json"))
    # No shell expansion, inherited live-install credentials, or raw driver output.
    child = subprocess.Popen(entry["argv"], env=env, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True, cwd=Path(args.state).resolve())
    try:
        code = child.wait(timeout=entry["timeout_seconds"])
    except subprocess.TimeoutExpired:
        os.killpg(child.pid, signal.SIGTERM)
        try:
            child.wait(timeout=5)
        except subprocess.TimeoutExpired:
            os.killpg(child.pid, signal.SIGKILL)
            child.wait()
        raise Refusal("case deadline exceeded; inspect retained resources") from None
    require(code == 0, "case executable failed")
    verified = validate_evidence(case, report, state)
    summary = {"adapter_sha256": hashlib.sha256(Path(entry["argv"][0]).read_bytes()).hexdigest(), "report": report.name, "sha256": hashlib.sha256(report.read_bytes()).hexdigest()}
    if "measurement" in verified:
        summary["measurement"] = verified["measurement"]
    return summary


def effective_exec_denied(kube):
    sa = f'system:serviceaccount:{kube.ns}:blerg-runner-agent-launcher'
    answer = kube.call("auth", "can-i", "create", "pods", "--subresource=exec", "--as", sa,
                       "--as-group=system:serviceaccounts", "--as-group=system:serviceaccounts:"+kube.ns,
                       "--as-group=system:authenticated", check=False)
    require(answer.returncode == 1 and answer.stdout.strip() == "no", "effective pods/exec denial not established")


def builtin(kube, case, values, state):
    if case["id"] == "placement-rbac":
        placement(kube, values)
        effective_exec_denied(kube)
        return {"required_affinity": True, "actual_placement": True, "exec_denied": True}
    name = "acceptance-writer-" + uuid.uuid4().hex[:12]
    pod = {"restartPolicy": "Never", "serviceAccountName": "blerg-runner-agent-launcher", "automountServiceAccountToken": True,
           "securityContext": {"runAsNonRoot": True, "runAsUser": 1000, "seccompProfile": {"type": "RuntimeDefault"}},
           "containers": [{"name": "tests", "image": f'{values["registry"]}/blerg-acceptance-tests:{values["tag"]}',
                           "imagePullPolicy": "IfNotPresent", "securityContext": {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}},
                           "resources": {"requests": {"cpu": "100m", "memory": "128Mi"}, "limits": {"cpu": "1", "memory": "512Mi"}},
                           "env": [{"name": k, "value": v} for k, v in {
                               "P3_TEST_NAMESPACE": kube.ns, "P3_TEST_EXCLUDE_NODES": ",".join(values["excluded_nodes"]),
                               "P3_TEST_POD_IMAGE": f'{values["registry"]}/blerg-runner-agent-enabled:{values["tag"]}'}.items()]}]}
    required_affinity(pod, values["excluded_nodes"])
    job = {"apiVersion": "batch/v1", "kind": "Job", "metadata": {"name": name, "namespace": kube.ns, "labels": {LABEL: state["run_id"]}},
           "spec": {"backoffLimit": 0, "activeDeadlineSeconds": 600, "template": {"metadata": {"labels": {LABEL: state["run_id"]}}, "spec": pod}}}
    created = json.loads(kube.call("create", "-f", "-", "-o", "json", data=job).stdout)
    kube.call("wait", "--for=condition=complete", "job/" + name, "--timeout=660s")
    placement(kube, values)
    # Keep the exact Job UID in results. No log/Secret dump and no generic delete.
    return {"job": name, "uid": created["metadata"]["uid"], "old_writer_gone": True, "replacement_uid": True, "replacement_cleanup": True}


def execute(args):
    ns = namespace(args.namespace)
    values = load_values(args.values)
    if args.dry_run:
        # No cluster discovery, client-side kubectl, kubeconfig reads, credentials or state writes.
        json.dump(render(ns, values, "render-only"), sys.stdout, indent=2)
        print()
        return 0
    require(args.context and args.state, "--context and private --state required")
    root = Path(args.state).resolve()
    require(not root.exists() or not root.is_symlink(), "state directory cannot be a symlink")
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    require(root.stat().st_mode & 0o077 == 0, "state directory must be private (0700)")
    results = {"schema": SCHEMA, "campaign_sha256": campaign_fingerprint(), "namespace": ns, "status": "not-run", "cases": [{"id": c["id"], "status": "not-run"} for c in cases()]}
    out = root / "results.json"
    if out.exists():
        os.replace(out, root / ("results-" + uuid.uuid4().hex + ".json"))
    atomic_json(out, results)
    if not args.deploy_only and not args.diagnostics_only:
        missing = driver_gaps(values)
        fixture = values.get("fixtures_file", "")
        if missing or not Path(fixture).is_file() or Path(fixture).stat().st_mode & 0o077:
            results["status"] = "blocked"
            results["missing_drivers"] = missing
            for case in results["cases"]:
                if case["id"] in missing:
                    case["status"] = "blocked"
            atomic_json(out, results)
            raise Refusal("campaign blocked before cluster access: required reviewed case drivers/private fixtures missing; see results.json")
    kube = Kube(args.context, ns)
    if args.resume:
        state = load_state(args)
        kube.owned(state)
        require(state.get("campaign_sha256") == campaign_fingerprint(), "campaign code/registry changed after deployment")
        require(state["values_sha256"] == deployment_fingerprint(values), "deployment values changed; registry/tag/placement/storage must remain immutable")
    else:
        require(not state_path(args).exists(), "state already exists; use --resume")
        state = {"schema": SCHEMA, "namespace": ns, "context": args.context, "run_id": uuid.uuid4().hex,
                 "values_sha256": deployment_fingerprint(values), "campaign_sha256": campaign_fingerprint()}
        # Read-only preflight: explicit storage classes, delete reclaim, eligible labels.
        for key in ("workspace_storage_class", "data_storage_class"):
            sc = kube.get("storageclass", values[key])
            require(sc.get("reclaimPolicy") == "Delete", "disposable classes require Delete reclaim policy; capacity is not quota proof")
        nodes = kube.get("nodes")["items"]
        require(set(values["excluded_nodes"]) <= {n["metadata"].get("labels", {}).get("kubernetes.io/hostname") for n in nodes},
                "excluded hostname labels do not match cluster nodes; refusing placeholder/mistyped values")
        require(any(n["metadata"].get("labels", {}).get("kubernetes.io/hostname") not in values["excluded_nodes"] and n["metadata"].get("labels", {}).get("kubernetes.io/hostname") for n in nodes), "no eligible node")
        deploy(kube, args, values, state)
    results.update(run_id=state["run_id"], namespace_uid=state["namespace_uid"])
    if args.deploy_only:
        results["status"] = "deployed-not-accepted"
        atomic_json(out, results)
        collect(kube, state, root)
        print("Deployed isolated install; acceptance NOT run. Bootstrap only this install, then --resume.")
        return 0
    if not args.diagnostics_only:
        try:
            validate_fixtures(values, state)
        except (Refusal, OSError, ValueError):
            results["status"] = "blocked"
            atomic_json(out, results)
            raise
    try:
        for spec, row in zip(cases(), results["cases"]):
            if args.diagnostics_only and spec["kind"] == "driver":
                row["status"] = "blocked"
                atomic_json(out, results)
                continue
            start = time.monotonic()
            row["status"] = "running"
            atomic_json(out, results)
            try:
                kube.owned(state)
                row["evidence"] = builtin(kube, spec, values, state) if spec["kind"] == "builtin" else run_driver(spec, values, args, state)
                placement(kube, values)
                effective_exec_denied(kube)
                row["status"] = "pass"
            except (Refusal, OSError, ValueError, subprocess.SubprocessError):
                row["status"] = "fail"
                # Continue to record every case; all must pass for campaign success.
            row["duration_seconds"] = round(time.monotonic() - start, 3)
            atomic_json(out, results)
        results["status"] = "pass" if all(c["status"] == "pass" for c in results["cases"]) else "fail"
    finally:
        atomic_json(out, results)
        collect(kube, state, root)
    return 0 if results["status"] == "pass" else 1


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
    parser.add_argument("--diagnostics-only", action="store_true", help="run only built-in checks; full campaign remains failed/incomplete")
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
    except (Refusal, OSError, ValueError, KeyError, subprocess.SubprocessError):
        # Credentials/URLs can occur in parser/subprocess exceptions; never echo them.
        print("REFUSED/FAILED: namespace, ownership, configuration, readiness or case evidence checks failed. See private results/journal; no automatic teardown.", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
