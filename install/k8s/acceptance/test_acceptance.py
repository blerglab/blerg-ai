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

    def test_active_smoke_needs_no_deferred_drivers(self):
        with tempfile.TemporaryDirectory() as directory:
            args = argparse.Namespace(namespace=self.ns, values=str(HERE/'values.example.json'), dry_run=None,
                                      context='fixture-context', state=directory, resume=True, deploy_only=False)
            state = dict(self.state, storage={'name':'default-fixture','uid':'class-uid'},
                         values_sha256=a.deployment_fingerprint(self.values), campaign_sha256=a.campaign_fingerprint())
            fake = unittest.mock.Mock()
            with patch.object(a,'Kube',return_value=fake), patch.object(a,'load_state',return_value=state), \
                 patch.object(a,'default_storage',return_value=state['storage']), patch.object(a,'placement'), \
                 patch.object(a,'effective_exec_denied', return_value={'status': 'pass', 'message': 'effective pods/exec denial established'}) as rbac, patch.object(a,'collect'), \
                 patch.object(a,'run_smoke',return_value={'assertions':'real smoke'}) as smoke:
                self.assertEqual(a.execute(args),0)
            smoke.assert_called_once()
            self.assertEqual(rbac.call_count,2)
            result=json.loads((Path(directory)/'results.json').read_text())
            self.assertEqual(result['status'],'pass')
            self.assertEqual([c['id'] for c in result['cases']],['cluster-smoke'])
            self.assertEqual(result['deferred']['card'],180)
            self.assertEqual(len(result['deferred']['cases']),25)
            self.assertEqual((Path(directory)/'results.json').stat().st_mode & 0o777,0o600)

    def test_case_catalog_has_all_six_measurements(self):
        cases = a.cases()
        self.assertEqual(len(cases), len({c["id"] for c in cases}))
        measurements = {c["id"] for c in cases if c["id"].startswith("measure-")}
        self.assertEqual(measurements, {f"measure-{n}-{p}" for n in (1, 5, 20) for p in ("ordinary", "metadata-heavy")})
        for c in cases:
            self.assertTrue(c["observations"])
            self.assertEqual((c["status"],c["release"],c["card"]),("deferred","0.5.1",180))

    def test_collection_discards_private_payloads(self):
        obj = {"kind": "Secret", "metadata": {"name": "fixture", "uid": "one"}, "data": {"key": "private-value"}, "spec": {"containers": [{"env": [{"value": "private-value"}]}]}, "status": {"message": "private-value", "phase": "Running"}}
        self.assertNotIn("private-value", json.dumps(a.metadata_only(obj)))

    def test_deployment_fingerprint_refuses_source_target_or_image_changes(self):
        new = copy.deepcopy(self.values)
        new["public_repos"][0] = "https://example.invalid/other.git"
        self.assertNotEqual(a.deployment_fingerprint(new), a.deployment_fingerprint(self.values))
        new = copy.deepcopy(self.values)
        new["push_targets"] = ["https://example.invalid/disposable-one.git", "https://example.invalid/disposable-two.git"]
        self.assertNotEqual(a.deployment_fingerprint(new), a.deployment_fingerprint(self.values))
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
            a.effective_exec_denied(kube)
        self.assertEqual(len(observed), 1)

    def test_unknown_excluded_node_refuses_before_namespace_creation(self):
        class Fake:
            ns = self.ns
            def get(fake, kind, name=None):
                if kind == "storageclass":
                    return {"items":[{"metadata":{"name":"default-fixture","uid":"class-uid","annotations":{"storageclass.kubernetes.io/is-default-class":"true"}},"provisioner":"fixture","reclaimPolicy":"Delete"}]}
                return {"items":[{"metadata":{"name":"eligible-worker","labels":{"kubernetes.io/hostname":"eligible-worker"}}}]}
            def call(fake, *args, **kwargs):
                raise AssertionError("mutation before excluded-node validation")
        with tempfile.TemporaryDirectory() as directory, patch.object(a,"Kube",return_value=Fake()), patch("sys.stderr",io.StringIO()):
            self.assertEqual(a.main(["run","--namespace",self.ns,"--context","fixture","--state",directory,"--deploy-only"]),2)
            self.assertFalse((Path(directory)/"namespace.json").exists())

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

    def test_default_storage_requires_one_delete_class_and_records_no_quota(self):
        fake=unittest.mock.Mock()
        sc={"metadata":{"name":"local-path","uid":"class-uid","annotations":{"storageclass.kubernetes.io/is-default-class":"true"}},"provisioner":"fixture","reclaimPolicy":"Delete"}
        fake.get.return_value={'items':[sc]}
        evidence=a.default_storage(fake)
        self.assertEqual(evidence['name'],'local-path')
        self.assertFalse(evidence['quota_enforced'])
        for choices in ([],[sc,copy.deepcopy(sc)],[{**sc,'reclaimPolicy':'Retain'}]):
            fake.get.return_value={'items':choices}
            with self.assertRaises(a.Refusal): a.default_storage(fake)
        doc=a.render(self.ns,self.values,'render-only')
        for resource in doc['items']:
            if resource['kind']=='PersistentVolumeClaim': self.assertNotIn('storageClassName',resource['spec'])
            if resource['kind']=='StatefulSet':
                for pvc in resource['spec']['volumeClaimTemplates']: self.assertNotIn('storageClassName',pvc['spec'])

    def test_public_parameters_never_push_back_to_sources(self):
        for replacement in ([{},{}],[],self.values['public_repos'][:1],[self.values['public_repos'][0]]*2,
                            ['https://token@example.invalid/a.git',self.values['public_repos'][1]],
                            ['https://@example.invalid/a.git',self.values['public_repos'][1]],
                            ['http://example.invalid/a.git',self.values['public_repos'][1]]):
            with self.assertRaises(a.Refusal): a.smoke_parameters(dict(self.values,public_repos=replacement))
        for targets in (self.values['public_repos'],[self.values['public_repos'][0]+'/',self.values['public_repos'][1]+'/' ],['https://example.invalid/disposable.git'],
                        ['https://example.invalid/disposable.git']*2):
            with self.assertRaises(a.Refusal): a.smoke_parameters(dict(self.values,push_targets=targets))
        a.smoke_parameters(dict(self.values,push_targets=['https://example.invalid/disposable1.git','https://example.invalid/disposable2.git']))

    def test_smoke_receipt_rejects_zero_exit_skip_and_partial_checks(self):
        marker='ACCEPTANCE_SMOKE_PASS clones=distinct explorer=list,search,status shell=open,close sync=fixture-push stop=jobs,secrets,tokens quotas=NOT_ENFORCED'
        complete=marker+'\n--- PASS: TestAcceptanceClusterSmoke (1.0s)\nPASS\n'
        self.assertFalse(a.smoke_receipt(complete)['quota_enforced'])
        for bad in ('PASS',marker,complete.replace(marker,''),complete+'--- SKIP: fixture\n',complete+'--- FAIL: fixture\n',complete+marker):
            with self.assertRaises(a.Refusal): a.smoke_receipt(bad)

    def test_smoke_job_is_scoped_and_uses_real_opt_in(self):
        marker='ACCEPTANCE_SMOKE_PASS clones=distinct explorer=list,search,status shell=open,close sync=fixture-push stop=jobs,secrets,tokens quotas=NOT_ENFORCED'
        commands=[]
        class Fake:
            ns=self.ns
            def call(fake,*args,data=None,**kwargs):
                commands.append((args,data))
                if data and data['kind']=='Job':
                    fake.job=copy.deepcopy(data);fake.job['metadata']['uid']='job-uid'
                    return argparse.Namespace(stdout=json.dumps(fake.job))
                return argparse.Namespace(stdout=marker+'\n--- PASS: TestAcceptanceClusterSmoke (1s)\n')
            def get(fake,kind,name):return dict(fake.job, status={'succeeded': 1})
        with tempfile.TemporaryDirectory() as directory:
            args=argparse.Namespace(state=directory)
            state=dict(self.state,storage={'name':'local-path','uid':'class-uid'})
            result=a.run_smoke(Fake(),self.values,state,args)
            self.assertEqual(result['target_mode'],'namespace-local-bare-fixture')
            self.assertEqual(result['members'],2)
            self.assertEqual(state['smoke_job']['uid'],'job-uid')
        service=next(data for _,data in commands if data and data['kind']=='Service')
        self.assertEqual(service['spec']['type'],'ClusterIP')
        job=next(data for _,data in commands if data and data['kind']=='Job')
        pod=job['spec']['template']['spec']
        self.assertEqual(pod['serviceAccountName'],'blerg-runner-agent-launcher')
        self.assertEqual(job['metadata']['namespace'],self.ns)
        self.assertIn('NotIn',json.dumps(pod['affinity']))
        env={e['name']:e for e in pod['containers'][0]['env']}
        self.assertEqual(env['P6_ACCEPTANCE_SMOKE']['value'],'1')
        self.assertEqual(env['BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS']['value'],'false')
        self.assertIn('valueFrom',env['TEST_DATABASE_URL'])
        self.assertNotIn('BLERG_RUNNER_SESSION_TOKEN',env)
        self.assertFalse(any('exec' in arg for args,_ in commands for arg in args))

    def test_failed_job_collects_safe_stage_receipt_without_completion_wait(self):
        receipt = [{"stage": "bootstrap", "outcome": "failed", "reason": "init_failed",
                    "started_at": "2026-10-24T00:00:00Z", "finished_at": "2026-10-24T00:00:01Z",
                    "secret": "private-value"}]
        raw = "private-value\n    smoke.go:1: ACCEPTANCE_LAUNCH_DIAGNOSTICS " + json.dumps(receipt) + "\n--- FAIL: TestAcceptanceClusterSmoke (1s)\n"
        commands = []
        class Fake:
            ns = self.ns
            def call(fake, *args, data=None, **kwargs):
                commands.append(args)
                if data and data['kind'] == 'Job':
                    fake.job = copy.deepcopy(data); fake.job['metadata']['uid'] = 'job-uid'
                    return argparse.Namespace(stdout=json.dumps(fake.job))
                return argparse.Namespace(stdout=raw)
            def get(fake, kind, name):
                return dict(fake.job, status={'conditions': [{'type':'Failed', 'status':'True'}]})
        with tempfile.TemporaryDirectory() as directory, patch.object(a.time, 'sleep', side_effect=AssertionError('failed Job waited')):
            with self.assertRaises(a.SmokeFailure) as caught:
                a.run_smoke(Fake(), self.values, dict(self.state, storage={'name':'local-path','uid':'class-uid'}), argparse.Namespace(state=directory))
        self.assertIn('stage bootstrap', str(caught.exception))
        self.assertNotIn('private-value', json.dumps(caught.exception.diagnostics))
        self.assertEqual(caught.exception.diagnostics[0]['reason'], 'init_failed')
        self.assertTrue(any(args[0] == 'logs' for args in commands))
        self.assertFalse(any(args[0] == 'wait' for args in commands))
        self.assertEqual(a.launch_diagnostics('ACCEPTANCE_LAUNCH_DIAGNOSTICS ' + json.dumps([{'stage':'private-value','outcome':'failed','reason':'stage_refused'}])), [])

    def test_queued_receipt_preserves_only_safe_kubernetes_reasons(self):
        for reason in ("AlreadyExists", "Forbidden", "Invalid", "SchedulingPolicyRefused", "TransportError"):
            raw = 'ACCEPTANCE_LAUNCH_DIAGNOSTICS ' + json.dumps([
                {'stage':'queued', 'outcome':'failed', 'reason':reason, 'message':'private-value'},
                {'stage':'queued', 'outcome':'failed', 'reason':'private-value'}])
            self.assertEqual(a.launch_diagnostics(raw), [{'stage':'queued', 'outcome':'failed', 'reason':reason}])

    def test_shell_receipt_preserves_only_safe_refusal_codes(self):
        for reason in ("session_not_ready", "binding_refused", "policy_unavailable", "policy_disabled", "authority_refused", "runtime_refused", "unavailable"):
            raw = 'ACCEPTANCE_LAUNCH_DIAGNOSTICS ' + json.dumps([
                {'stage':'shell_open', 'outcome':'failed', 'reason':reason, 'message':'private-value'},
                {'stage':'shell_open', 'outcome':'failed', 'reason':'private-value'}])
            self.assertEqual(a.launch_diagnostics(raw), [{'stage':'shell_open', 'outcome':'failed', 'reason':reason}])

    def test_capacity_overlay_is_narrow_and_never_edits_production(self):
        spec=importlib.util.spec_from_file_location('smoke_build',HERE/'smoke-build.py')
        build=importlib.util.module_from_spec(spec);spec.loader.exec_module(build)
        file=HERE.parents[2]/'runner/internal/clusterworkspace/git_linux.go'
        source=file.read_text()
        patched=build.overlay(source)
        self.assertNotEqual(patched,source)
        self.assertEqual(patched.count("return newDiskGit(public, private, session, false, expected...)"),1)
        self.assertEqual(patched.replace('return newDiskGit(public, private, session, false, expected...)',
                                         'return newDiskGit(public, private, session, true, expected...)'),source)
        self.assertEqual(file.read_text(),source)
        for changed in ('unrecognized seam',source+source):
            with self.assertRaises(ValueError):build.overlay(changed)
        gate = (HERE.parents[2]/'runner/internal/protocol/workspacegate.go').read_text()
        self.assertIn('ClusterWorkspaceEnabled = true', gate)
        self.assertIn('MacOSHostWorkspaceEnabled = false', gate)

    def test_scripted_turn_commits_all_real_git_members(self):
        import subprocess
        spec=importlib.util.spec_from_file_location('scripted_engine',HERE/'scripted-engine.py')
        engine=importlib.util.module_from_spec(spec);spec.loader.exec_module(engine)
        with tempfile.TemporaryDirectory() as directory:
            roots=[Path(directory)/name for name in ('one','two','three')]
            before=[]
            for root in roots:
                root.mkdir()
                for args in (['init','-q'],['-c','user.name=Acceptance','-c','user.email=acceptance@example.invalid','commit','--allow-empty','-qm','initial']):
                    subprocess.run(['git','-C',str(root),*args],check=True,stdout=subprocess.DEVNULL)
                before.append(subprocess.check_output(['git','-C',str(root),'rev-parse','HEAD']))
            with patch('sys.stdout',io.StringIO()): engine.turn('initial workspace guide',directory)
            self.assertTrue(all(not (root/'acceptance-smoke.txt').exists() for root in roots))
            with patch('sys.stdout',io.StringIO()): engine.turn('commit smoke marker in every member',directory)
            for root,old in zip(roots,before):
                self.assertNotEqual(subprocess.check_output(['git','-C',str(root),'rev-parse','HEAD']),old)
                self.assertIn(b'acceptance smoke',subprocess.check_output(['git','-C',str(root),'show','HEAD:acceptance-smoke.txt']))


if __name__ == "__main__":
    unittest.main()
