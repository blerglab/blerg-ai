"""Offline tests: API calls are fakes. Nothing here discovers a kube context."""
import argparse
import copy
import importlib.util
import io
import hashlib
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("acceptance", HERE / "acceptance.py")
a = importlib.util.module_from_spec(spec)
spec.loader.exec_module(a)


class Safety(unittest.TestCase):
    def setUp(self):
        self.values = a.load_values(HERE / "values.example.json")
        self.ns = "acceptance-fixture"
        self.state = {"namespace": self.ns, "context": "fixture-context", "namespace_uid": "namespace-uid", "run_id": "a" * 32}

    def test_namespaces_fail_before_cluster_access(self):
        for ns in ("blerg", "default", "acceptance-", "acceptance-../blerg", "Acceptance-one", "acceptance-a/other", "acceptance-" + "a" * 60):
            with self.subTest(ns=ns), patch.object(a, "Kube", side_effect=AssertionError("cluster contacted")), patch("sys.stderr", io.StringIO()):
                self.assertEqual(a.main(["run", "--namespace", ns, "--dry-run"]), 2)

    def test_dry_run_has_zero_tools_or_state(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(a.subprocess, "run", side_effect=AssertionError("tool invoked")), patch("sys.stdout", io.StringIO()) as output:
            state = Path(directory) / "absent"
            self.assertEqual(a.main(["run", "--namespace", self.ns, "--state", str(state), "--dry-run=server-free"]), 0)
            self.assertFalse(state.exists())
            self.assertEqual(json.loads(output.getvalue())["kind"], "List")

    def test_render_is_private_scoped_and_production_rbac(self):
        doc = a.render(self.ns, self.values, "render-only")
        items = doc["items"]
        self.assertEqual(sum(d["kind"] == "Namespace" for d in items), 1)
        for d in items:
            if d["kind"] != "Namespace":
                self.assertEqual(d["metadata"]["namespace"], self.ns)
            self.assertNotIn(d["kind"], ("Ingress", "ClusterRole", "ClusterRoleBinding", "PersistentVolume", "StorageClass"))
            if d["kind"] == "Service":
                self.assertEqual(d["spec"].get("type", "ClusterIP"), "ClusterIP")
                self.assertNotIn("externalIPs", d["spec"])
            if d["kind"] in ("Deployment", "StatefulSet"):
                pod = d["spec"]["template"]["spec"]
                self.assertNotIn("hostNetwork", pod)
                self.assertNotIn("nodeName", pod)
                terms = pod["affinity"]["nodeAffinity"]["requiredDuringSchedulingIgnoredDuringExecution"]["nodeSelectorTerms"]
                for term in terms:
                    self.assertIn({"key": "kubernetes.io/hostname", "operator": "NotIn", "values": self.values["excluded_nodes"]}, term["matchExpressions"])
                for c in pod["containers"]:
                    self.assertIn(":", c["image"])
                    self.assertNotIn(":latest", c["image"])
                    self.assertIn("limits", c["resources"])
        core = next(d for d in items if d["kind"] == "Deployment" and d["metadata"]["name"] == "blerg-core")
        core_env = core["spec"]["template"]["spec"]["containers"][0]["env"]
        self.assertIn({"name": "BLERG_SETUP_ALLOW_LOCAL", "value": "true"}, core_env)
        self.assertIn({"name": "BLERG_SETUP_PUBLISHED_ADDR", "value": "localhost"}, core_env)
        self.assertIn({"name": "BLERG_CORE_BEHIND_PROXY", "value": "false"}, core_env)
        limits = next(d for d in items if d["kind"] == "LimitRange")["spec"]["limits"][0]
        self.assertTrue(set(limits["max"]) <= set(limits["default"]))
        self.assertTrue(set(limits["max"]) <= set(limits["defaultRequest"]))
        role = next(d for d in items if d["kind"] == "Role")
        production = next(d for d in a.production_yaml("cluster-runtime.yaml") if d["kind"] == "Role")
        self.assertEqual(role["rules"], production["rules"])
        self.assertNotIn("pods/exec", json.dumps(role))
        config = next(d for d in items if d["kind"] == "ConfigMap" and d["metadata"]["name"] == "blerg-config")["data"]
        self.assertEqual(config["BLERG_RUNNER_AGENT_NAMESPACE"], self.ns)
        self.assertIn("." + self.ns + ".svc.", config["BLERG_RUNNER_INTERNAL_URL"])
        self.assertNotIn("${", json.dumps(doc))
        self.assertNotIn("blerg-runner-sessions", json.dumps(doc))
        runner = next(d for d in items if d["kind"] == "Deployment" and d["metadata"]["name"] == "blerg-runner")
        env = runner["spec"]["template"]["spec"]["containers"][0]["env"]
        self.assertIn({"name": "BLERG_RUNNER_POD_EXCLUDE_NODES", "value": ",".join(self.values["excluded_nodes"])}, env)

    def test_affinity_strengthens_every_or_term(self):
        pod = {"affinity": {"podAffinity": {"preserve": True}, "nodeAffinity": {"requiredDuringSchedulingIgnoredDuringExecution": {"nodeSelectorTerms": [{}, {"matchExpressions": [{"key": "disk", "operator": "Exists"}]}]}}}}
        a.required_affinity(pod, ["excluded-node"])
        self.assertTrue(pod["affinity"]["podAffinity"]["preserve"])
        terms = pod["affinity"]["nodeAffinity"]["requiredDuringSchedulingIgnoredDuringExecution"]["nodeSelectorTerms"]
        self.assertEqual(len(terms), 2)
        self.assertTrue(all(any(e.get("operator") == "NotIn" for e in t["matchExpressions"]) for t in terms))
        with self.assertRaises(a.Refusal):
            a.required_affinity({"nodeName": "excluded-node"}, ["excluded-node"])

    def test_missing_cases_block_before_any_cluster_access(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(a, "Kube", side_effect=AssertionError("cluster contacted")), patch("sys.stderr", io.StringIO()):
            self.assertEqual(a.main(["run", "--namespace", self.ns, "--context", "fixture", "--state", directory]), 2)
            result = json.loads((Path(directory) / "results.json").read_text())
            self.assertEqual(result["status"], "blocked")
            self.assertEqual(len(result["missing_drivers"]), 23)
            self.assertEqual((Path(directory) / "results.json").stat().st_mode & 0o777, 0o600)

    def test_case_catalog_has_all_six_measurements(self):
        cases = a.cases()
        self.assertEqual(len(cases), len({c["id"] for c in cases}))
        measurements = {c["id"] for c in cases if c["id"].startswith("measure-")}
        self.assertEqual(measurements, {f"measure-{n}-{p}" for n in (1, 5, 20) for p in ("ordinary", "metadata-heavy")})
        for c in cases:
            self.assertTrue(c["observations"])

    def test_receipt_cannot_upgrade_exit_zero_or_stale_install(self):
        case = {"id": "fixture", "observations": ["real-proof"]}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "receipt.json"
            for result in ({"status": "pass"}, {"status": "pass", "case": "fixture", **self.state, "observations": {"real-proof": {"passed": True, "evidence": "sample"}}}):
                result.pop("namespace_uid", None)
                a.atomic_json(path, result)
                with self.assertRaises(a.Refusal):
                    a.validate_evidence(case, path, self.state)
            result = {"status": "pass", "case": "fixture", "namespace_uid": self.state["namespace_uid"], "run_id": self.state["run_id"], "observations": {"real-proof": {"passed": True, "evidence": "sample"}}}
            artifact = Path(directory) / "sample.json"
            artifact.write_text('{"sample":"local fixture"}')
            result["observations"]["real-proof"]["evidence"] = [{"path": artifact.name, "sha256": hashlib.sha256(artifact.read_bytes()).hexdigest()}]
            a.atomic_json(path, result)
            self.assertEqual(a.validate_evidence(case, path, self.state), result)
            result["observations"]["real-proof"]["evidence"][0]["sha256"] = "wrong"
            a.atomic_json(path, result)
            with self.assertRaises(a.Refusal):
                a.validate_evidence(case, path, self.state)
            result["observations"]["real-proof"]["evidence"][0]["sha256"] = hashlib.sha256(artifact.read_bytes()).hexdigest()
            result["observations"]["real-proof"]["passed"] = False
            a.atomic_json(path, result)
            with self.assertRaises(a.Refusal):
                a.validate_evidence(case, path, self.state)

    def test_collection_discards_private_payloads(self):
        obj = {"kind": "Secret", "metadata": {"name": "fixture", "uid": "one"}, "data": {"key": "private-value"}, "spec": {"containers": [{"env": [{"value": "private-value"}]}]}, "status": {"message": "private-value", "phase": "Running"}}
        self.assertNotIn("private-value", json.dumps(a.metadata_only(obj)))

    def test_deployment_fingerprint_allows_bootstrap_not_retargeting(self):
        new = copy.deepcopy(self.values)
        new["case_drivers"] = {"fixture": {"argv": ["/fixture"], "timeout_seconds": 1}}
        self.assertEqual(a.deployment_fingerprint(new), a.deployment_fingerprint(self.values))
        new["tag"] = "other-tag"
        self.assertNotEqual(a.deployment_fingerprint(new), a.deployment_fingerprint(self.values))

    def test_delete_uses_uid_precondition_and_never_force(self):
        kube = a.Kube("fixture", self.ns)
        captured = []
        def call(*args, **kwargs):
            captured.append((args, json.loads(Path(args[-1]).read_text())))
        with patch.object(kube, "call", side_effect=call):
            kube.delete_uid("namespaces", self.ns, "namespace-uid")
        args, body = captured[0]
        self.assertEqual(body["preconditions"], {"uid": "namespace-uid"})
        self.assertEqual(args[2], "/api/v1/namespaces/" + self.ns)
        self.assertNotIn("--force", args)
        with self.assertRaises(a.Refusal):
            kube.delete_uid("namespaces", "blerg", "foreign")

    def test_teardown_proves_namespace_and_pv_identity(self):
        parent = self
        class Fake(a.Kube):
            def __init__(self, context, ns):
                super().__init__(context, ns)
                self.deleted = []
                self.ns_live = True
                self.changed = False
            def get(self, kind, name=None):
                if kind == "namespace":
                    return {"metadata": {"uid": parent.state["namespace_uid"], "labels": {a.LABEL: parent.state["run_id"]}}}
                if kind == "persistentvolumeclaims":
                    return {"items": [{"metadata": {"uid": "claim-uid"}, "spec": {"volumeName": "pvc-random-name"}}]}
                return {"metadata": {"uid": "pv-uid"}, "spec": {"claimRef": {"namespace": parent.ns, "uid": "other-claim" if self.changed else "claim-uid"}}}
            def optional(self, kind, name):
                return self.get(kind, name) if kind != "namespace" or self.ns_live else None
            def delete_uid(self, kind, name, uid):
                self.deleted.append((kind, name, uid))
                if kind == "namespaces":
                    self.ns_live = False
            def call(self, *args, **kwargs):
                pass
        for rebound in (False, True):
            with tempfile.TemporaryDirectory() as directory:
                args = argparse.Namespace(namespace=self.ns, context="fixture-context", state=directory, dry_run=False)
                a.atomic_json(a.state_path(args), self.state)
                fake = Fake(args.context, self.ns)
                original = fake.delete_uid
                def delete(kind, name, uid):
                    original(kind, name, uid)
                    if kind == "namespaces":
                        fake.changed = rebound
                fake.delete_uid = delete
                with patch.object(a, "Kube", return_value=fake), patch("sys.stdout", io.StringIO()):
                    if rebound:
                        with self.assertRaises(a.Refusal):
                            a.teardown(args)
                        self.assertEqual(len(fake.deleted), 1)
                    else:
                        self.assertEqual(a.teardown(args), 0)
                        self.assertEqual(fake.deleted, [("namespaces", self.ns, "namespace-uid"), ("persistentvolumes", "pvc-random-name", "pv-uid")])

    def test_teardown_namespace_replacement_or_missing_journal_refuses(self):
        with tempfile.TemporaryDirectory() as directory:
            args = argparse.Namespace(namespace=self.ns, context="fixture-context", state=directory, dry_run=False)
            a.atomic_json(a.state_path(args), self.state)
            kube = a.Kube(args.context, self.ns)
            with patch.object(kube, "get", return_value={"metadata": {"uid": "foreign-uid", "labels": {a.LABEL: self.state["run_id"]}}}):
                with self.assertRaises(a.Refusal):
                    kube.owned(self.state)
            with patch.object(a, "Kube", return_value=kube), patch.object(kube, "optional", return_value=None):
                with self.assertRaises(a.Refusal):
                    a.teardown(args)


    def test_effective_exec_denial_checks_subresource_and_serviceaccount_groups(self):
        kube = a.Kube("fixture", self.ns)
        observed = []
        def call(*args, **kwargs):
            observed.append(args)
            self.assertEqual(args[:5], ("auth", "can-i", "create", "pods", "--subresource=exec"))
            for group in ("system:serviceaccounts", "system:serviceaccounts:"+self.ns, "system:authenticated"):
                self.assertIn("--as-group="+group, args)
            return argparse.Namespace(returncode=1, stdout="no\n")
        with patch.object(a, "placement"), patch.object(kube, "call", side_effect=call):
            a.builtin(kube, {"id":"placement-rbac"}, self.values, self.state)
        self.assertEqual(len(observed), 1)

    def test_unknown_excluded_node_refuses_before_namespace_creation(self):
        class Fake:
            ns = self.ns
            def get(fake, kind, name=None):
                if kind == "storageclass":
                    return {"reclaimPolicy":"Delete"}
                return {"items":[{"metadata":{"name":"eligible-worker","labels":{"kubernetes.io/hostname":"eligible-worker"}}}]}
            def call(fake, *args, **kwargs):
                raise AssertionError("mutation before excluded-node validation")
        with tempfile.TemporaryDirectory() as directory, patch.object(a,"Kube",return_value=Fake()), patch("sys.stderr",io.StringIO()):
            self.assertEqual(a.main(["run","--namespace",self.ns,"--context","fixture","--state",directory,"--deploy-only"]),2)
            self.assertFalse((Path(directory)/"namespace.json").exists())

    def test_fixtures_cannot_retarget_live_or_other_install(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)/"fixtures.json"
            values = {**self.values, "fixtures_file": str(path)}
            fixture = {"install":{**self.state,"urls":{"core":"http://localhost:18081","board":"http://localhost:18082","runner":"http://localhost:18083"}}}
            a.atomic_json(path,fixture)
            a.validate_fixtures(values,self.state)
            fixture["install"]["urls"]["runner"] = "http://blerg-runner.blerg.svc.cluster.local:8080"
            a.atomic_json(path,fixture)
            with self.assertRaises(a.Refusal):
                a.validate_fixtures(values,self.state)
            fixture["install"]["urls"]["runner"] = "http://localhost:18083"
            fixture["install"]["namespace_uid"] = "other-install"
            a.atomic_json(path,fixture)
            with self.assertRaises(a.Refusal):
                a.validate_fixtures(values,self.state)

    def test_partial_deploy_journals_before_apply(self):
        with tempfile.TemporaryDirectory() as directory:
            args = argparse.Namespace(state=directory)
            class Fake:
                ns = self.ns
                def call(fake, *args, **kwargs):
                    if args[0] == "create":
                        return argparse.Namespace(stdout=json.dumps({"metadata": {"uid": "namespace-uid"}}))
                    if args[0] == "apply":
                        journal = json.loads((Path(directory) / "namespace.json").read_text())
                        self.assertEqual(journal["namespace_uid"], "namespace-uid")
                        self.assertTrue(all(d["metadata"]["namespace"] == self.ns for d in kwargs["data"]["items"]))
                        raise a.Refusal("simulated apply failure")
                    self.fail("unexpected command")
            with self.assertRaises(a.Refusal):
                a.deploy(Fake(), args, self.values, copy.deepcopy(self.state))
            self.assertTrue((Path(directory) / "namespace.json").exists())

    def test_teardown_retry_keeps_previous_pv_journal(self):
        with tempfile.TemporaryDirectory() as directory:
            args = argparse.Namespace(namespace=self.ns, context=self.state["context"], state=directory, dry_run=False)
            state = {**self.state, "pvs": [{"name": "pvc-fixture", "uid": "pv-uid", "claim_uid": "claim-uid"}]}
            a.atomic_json(a.state_path(args), state)
            kube = a.Kube(args.context, self.ns)
            deleted = []
            obj = {"metadata": {"uid": state["namespace_uid"], "labels": {a.LABEL: state["run_id"]}}}
            pv = {"metadata": {"uid": "pv-uid"}, "spec": {"claimRef": {"namespace": self.ns, "uid": "claim-uid"}}}
            def optional(kind, name):
                return (None if deleted else obj) if kind == "namespace" else pv
            def get(kind, name=None):
                return obj if kind == "namespace" else {"items": []}
            with patch.object(a, "Kube", return_value=kube), patch.object(kube, "optional", side_effect=optional), patch.object(kube, "get", side_effect=get), patch.object(kube, "call"), patch.object(kube, "delete_uid", side_effect=lambda *x: deleted.append(x)), patch("sys.stdout", io.StringIO()):
                self.assertEqual(a.teardown(args), 0)
            self.assertEqual(len(deleted), 2)
            self.assertEqual(deleted[-1], ("persistentvolumes", "pvc-fixture", "pv-uid"))

    def test_measurements_need_target_and_cold_warm_samples(self):
        case = {"id": "measure-20-metadata-heavy", "observations": []}
        measurement = {"members": 20, "profile": "metadata-heavy", "image_id": "digest", "commit": "commit", "storage_class": "class", "csi_driver": "driver", "node": "worker"}
        keys = ("prepare_ms", "snapshot_ms", "sync_ms", "stop_ms", "control_ms", "bytes", "inodes", "cpu_seconds", "peak_rss_bytes")
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "measurement.json"
            result = {"case": case["id"], "namespace_uid": self.state["namespace_uid"], "run_id": self.state["run_id"], "status": "pass", "observations": {}, "measurement": measurement}
            a.atomic_json(path, result)
            with self.assertRaises(a.Refusal):
                a.validate_evidence(case, path, self.state)
            measurement["samples"] = [{**dict.fromkeys(keys, i), "cold": i == 1} for i in (1, 2, 3)]
            a.atomic_json(path, result)
            parsed = a.validate_evidence(case, path, self.state)
            self.assertEqual(parsed["measurement"]["latency_percentiles"]["sync_ms"], {"50": 2, "95": 3, "99": 3})
            measurement["samples"][0]["sync_ms"] = float("inf")
            a.atomic_json(path, result)
            with self.assertRaises(a.Refusal):
                a.validate_evidence(case, path, self.state)


if __name__ == "__main__":
    unittest.main()
