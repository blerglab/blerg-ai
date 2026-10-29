"""Offline authorization and diagnostic regressions; kubectl is always fake."""
import argparse
import ast
from contextlib import ExitStack
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("acceptance_authorization", HERE / "acceptance.py")
a = importlib.util.module_from_spec(spec)
spec.loader.exec_module(a)

LIMITATION = "not-enforced: cluster authorization is AlwaysAllow (RBAC off)"


class Authorization(unittest.TestCase):
    def setUp(self):
        self.ns = "acceptance-auth-fixture"
        self.values = a.load_values(HERE / "values.example.json")
        self.state = {"namespace": self.ns, "context": "fixture-context", "namespace_uid": "namespace-uid",
                      "run_id": "a" * 32, "storage": {"name": "fixture", "uid": "class-uid"},
                      "values_sha256": a.deployment_fingerprint(self.values),
                      "campaign_sha256": a.campaign_fingerprint()}
        self.commands = []

    def kubectl(self, answers):
        answers = iter(answers)

        def run(command, **kwargs):
            self.commands.append(command)
            self.assertEqual(command[:7], ["kubectl", "--context", "fixture-context", "--namespace",
                                          self.ns, "--request-timeout=30s", "auth"])
            self.assertEqual(command[7], "can-i")
            self.assertIn("system:serviceaccount:" + self.ns + ":blerg-runner-agent-launcher", command)
            for group in ("system:serviceaccounts", "system:serviceaccounts:" + self.ns, "system:authenticated"):
                self.assertIn("--as-group=" + group, command)
            self.assertIsNone(kwargs["input"])
            answer = next(answers)
            if isinstance(answer, BaseException):
                raise answer
            code, stdout = answer
            return subprocess.CompletedProcess(command, code, stdout, "private diagnostic must not persist")

        return run

    def check(self, answers):
        with patch.object(a.subprocess, "run", side_effect=self.kubectl(answers)):
            return a.effective_exec_denied(a.Kube("fixture-context", self.ns))

    def execute(self, answers, *, errors=None, main=False, deploy=False):
        errors = errors or {}
        with tempfile.TemporaryDirectory() as directory, ExitStack() as stack:
            args = argparse.Namespace(namespace=self.ns, values=str(HERE / "values.example.json"), dry_run=None,
                                      context="fixture-context", state=directory, resume=not deploy, deploy_only=False)
            for name, value in (("load_state", self.state), ("default_storage", self.state["storage"]),
                                ("placement", None), ("collect", None), ("deploy", None)):
                stack.enter_context(patch.object(a, name, return_value=value, side_effect=errors.get(name)))
            stack.enter_context(patch.object(a.Kube, "owned", side_effect=errors.get("owned")))
            stack.enter_context(patch.object(a.Kube, "get", return_value={"items": [
                {"metadata": {"labels": {"kubernetes.io/hostname": node}}}
                for node in self.values["excluded_nodes"] + ["eligible-worker"]]}))
            stack.enter_context(patch.object(a.subprocess, "run", side_effect=self.kubectl(answers)))
            smoke = stack.enter_context(patch.object(a, "run_smoke", return_value={"assertions": "complete"},
                                                    side_effect=errors.get("run_smoke")))
            stack.enter_context(patch("sys.stderr", io.StringIO()))
            if main:
                code = a.main(["run", "--namespace", self.ns, "--context", "fixture-context", "--state", directory,
                               *([] if deploy else ["--resume"])])
            else:
                code = a.execute(args)
            result = json.loads((Path(directory) / "results.json").read_text())
            self.assertEqual((Path(directory) / "results.json").stat().st_mode & 0o777, 0o600)
            return code, result, smoke

    def test_rbac_denies_exec(self):
        check = self.check([(1, "no\n")])
        self.assertEqual(check["status"], "pass")
        self.assertEqual(len(self.commands), 1)
        self.assertEqual(self.commands[0][8:11], ["create", "pods", "--subresource=exec"])

    def test_always_allow_is_positively_probed_without_performing_actions(self):
        check = self.check([(0, "yes\n")] * 3)
        self.assertEqual(check, {"status": "pass-with-limitation", "message": LIMITATION})
        self.assertEqual(len(self.commands), 3)
        self.assertIn("--namespace=kube-system", self.commands[1])
        self.assertEqual(self.commands[1][8:10], ["get", "secrets"])
        self.assertEqual(self.commands[2][8:10], ["create", "clusterroles.rbac.authorization.k8s.io"])

    def test_exec_grant_without_both_unrelated_grants_fails(self):
        for probes in ([(1, "no"), (1, "no")], [(0, "yes"), (1, "no")], [(1, "no"), (0, "yes")]):
            with self.subTest(probes=probes), self.assertRaises(a.Refusal):
                self.check([(0, "yes"), *probes])

    def test_auth_errors_and_inconsistent_answers_fail_closed(self):
        invalid = [(2, ""), (1, ""), (0, "no"), (1, "yes"), (0, "yes\nno"), (0, "unknown")]
        for answer in invalid:
            for index in range(3):
                with self.subTest(answer=answer, index=index), self.assertRaises(a.Refusal):
                    self.check([(0, "yes")] * index + [answer] + [(0, "yes")] * (2 - index))

    def test_always_allow_runs_full_smoke_and_cannot_report_plain_pass(self):
        code, result, smoke = self.execute([(0, "yes")] * 6)
        self.assertEqual(code, 0)
        smoke.assert_called_once()
        row = result["cases"][0]
        self.assertEqual((result["status"], row["status"]), ("pass-rbac-not-enforced",) * 2)
        self.assertEqual(row["checks"]["exec_denied"], {"status": "pass-with-limitation", "message": LIMITATION})
        self.assertEqual(row["evidence"]["exec_denied"], LIMITATION)
        self.assertEqual(row["evidence"]["assertions"], "complete")
        self.assertIn(LIMITATION, result["limitations"])
        self.assertIn("Test image omits capacity admission; production capacity checks remain enabled.", result["limitations"])
        self.assertEqual(len(self.commands), 6)

    def test_rbac_success_remains_plain_pass(self):
        code, result, smoke = self.execute([(1, "no")] * 2)
        self.assertEqual((code, result["status"]), (0, "pass"))
        self.assertEqual(result["cases"][0]["evidence"]["exec_denied"], True)
        smoke.assert_called_once()

    def test_limitation_survives_authorization_changes_between_checks(self):
        for answers in ([(0, "yes")] * 3 + [(1, "no")], [(1, "no")] + [(0, "yes")] * 3):
            with self.subTest(answers=answers):
                code, result, _ = self.execute(answers)
                self.assertEqual((code, result["status"]), (0, "pass-rbac-not-enforced"))
                self.assertEqual(result["cases"][0]["evidence"]["exec_denied"], LIMITATION)

    def test_exec_permitted_on_rbac_fails_before_or_after_smoke(self):
        for before in (True, False):
            with self.subTest(before=before):
                answers = ([] if before else [(1, "no")]) + [(0, "yes"), (1, "no"), (1, "no")]
                code, result, smoke = self.execute(answers)
                self.assertEqual((code, result["status"]), (1, "fail"))
                self.assertEqual(smoke.call_count, 0 if before else 1)
                error = result["errors"][0]
                self.assertEqual(error["type"], "Refusal")
                self.assertIn("pods/exec", error["message"])
                self.assertEqual(result["cases"][0]["checks"]["exec_denied"]["status"], "fail")

    def test_smoke_failure_is_not_hidden_by_always_allow(self):
        code, result, _ = self.execute([(0, "yes")] * 3,
                                       errors={"run_smoke": a.Refusal("real-pod smoke did not complete every assertion")})
        self.assertEqual((code, result["status"]), (1, "fail"))
        self.assertEqual(result["errors"][0]["message"], "real-pod smoke did not complete every assertion")
        self.assertEqual(result["cases"][0]["checks"]["exec_denied"]["status"], "pass-with-limitation")

    def test_launch_failure_receipt_survives_in_results(self):
        diagnostics = [{"stage": "writer_fence", "outcome": "failed", "reason": "stage_refused",
                        "started_at": "2026-10-24T00:00:00Z", "finished_at": "2026-10-24T00:00:01Z"}]
        code, result, _ = self.execute([(1, "no")], errors={"run_smoke": a.SmokeFailure(diagnostics)})
        self.assertEqual((code, result["status"]), (1, "fail"))
        self.assertIn("stage writer_fence", result["errors"][0]["message"])
        self.assertEqual(result["cases"][0]["evidence"]["launch_stages"], diagnostics)

    def test_queued_kubernetes_reason_survives_in_results(self):
        for reason in ("AlreadyExists", "Forbidden", "Invalid"):
            diagnostics = [{"stage": "queued", "outcome": "failed", "reason": reason}]
            code, result, _ = self.execute([(1, "no")], errors={"run_smoke": a.SmokeFailure(diagnostics)})
            self.assertEqual((code, result["status"]), (1, "fail"))
            self.assertIn("stage queued", result["errors"][0]["message"])
            self.assertEqual(result["cases"][0]["evidence"]["launch_stages"], diagnostics)

    def test_each_exception_type_is_recorded_without_private_details(self):
        private = "https://example.invalid/private?token=private-value"
        errors = [a.Refusal("smoke Job identity changed"), OSError(5, "private-value", private),
                  ValueError("private-value " + private), KeyError(private), TypeError("private-value"),
                  subprocess.TimeoutExpired(["kubectl", private, "private-value"], 30, output="private-value"),
                  subprocess.CalledProcessError(7, ["kubectl", private], stderr="private-value"),
                  json.JSONDecodeError("invalid JSON", '"private-value"', 1)]
        for error in errors:
            with self.subTest(type=type(error).__name__):
                code, result, _ = self.execute([(1, "no")], errors={"run_smoke": error})
                self.assertEqual((code, result["status"]), (1, "fail"))
                detail = result["errors"][0]
                self.assertEqual(detail["type"], type(error).__name__)
                self.assertTrue(detail["message"])
                self.assertNotIn("private-value", json.dumps(result))
                self.assertNotIn(private, json.dumps(result))

    def test_preflight_deployment_and_collection_errors_are_recorded(self):
        for stage in ("load_state", "default_storage", "owned", "placement", "deploy", "collect"):
            with self.subTest(stage=stage):
                code, result, _ = self.execute([(1, "no")] * 2, main=True, deploy=stage == "deploy",
                                               errors={stage: a.Refusal("namespace identity/ownership changed; refusing")})
                self.assertNotEqual(code, 0)
                self.assertEqual(result["status"], "fail")
                self.assertEqual(result["errors"][0]["type"], "Refusal")
                self.assertIn("ownership", result["errors"][0]["message"])

    def test_collection_failure_preserves_the_original_smoke_error(self):
        code, result, _ = self.execute([(1, "no")], errors={
            "run_smoke": a.Refusal("smoke Job identity changed"),
            "collect": a.Refusal("PV claim identity differs")})
        self.assertEqual((code, result["status"]), (1, "fail"))
        self.assertEqual([e["message"] for e in result["errors"]],
                         ["smoke Job identity changed", "PV claim identity differs"])

    def test_authorization_probe_exception_is_recorded_and_never_downgraded(self):
        error = subprocess.TimeoutExpired(["kubectl", "https://example.invalid/private-value"], 30)
        code, result, smoke = self.execute([(0, "yes"), error])
        self.assertEqual((code, result["status"]), (1, "fail"))
        smoke.assert_not_called()
        self.assertEqual(result["errors"][0]["type"], "TimeoutExpired")
        self.assertNotIn("private-value", json.dumps(result))

    def test_refusal_messages_are_owned_static_strings(self):
        # Refusal is the only exception whose original message is safe to keep.
        # Enforce its contract: no configuration, tool output or URLs in messages.
        tree = ast.parse((HERE / "acceptance.py").read_text())
        for function in ast.walk(tree):
            if not isinstance(function, (ast.FunctionDef, ast.AsyncFunctionDef)):
                continue
            for call in ast.walk(function):
                if not isinstance(call, ast.Call) or not isinstance(call.func, ast.Name):
                    continue
                if call.func.id == "require":
                    message = call.args[1]
                elif call.func.id == "Refusal" and function.name != "require":
                    message = call.args[0]
                else:
                    continue
                self.assertIsInstance(message, ast.Constant)
                self.assertIsInstance(message.value, str)
                self.assertNotIn("://", message.value)

    def test_main_records_invalid_configuration_without_tool_access(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(a, "Kube", side_effect=AssertionError("tool access")), \
                patch("sys.stderr", io.StringIO()):
            values = Path(directory) / "values.json"
            values.write_text('{"private-value":')
            code = a.main(["run", "--namespace", self.ns, "--context", "fixture-context", "--state", directory,
                           "--values", str(values)])
            self.assertNotEqual(code, 0)
            result = json.loads((Path(directory) / "results.json").read_text())
            self.assertEqual(result["status"], "fail")
            self.assertEqual(result["errors"][0]["type"], "JSONDecodeError")
            self.assertNotIn("private-value", json.dumps(result))

    def test_main_boundary_records_errors_and_preserves_existing_results(self):
        for action in ("run", "collect", "teardown"):
            with self.subTest(action=action), tempfile.TemporaryDirectory() as directory, \
                    patch.object(a, "execute", side_effect=KeyError("private-value")), \
                    patch.object(a, "load_state", side_effect=KeyError("private-value")), \
                    patch("sys.stderr", io.StringIO()):
                out = Path(directory) / "results.json"
                a.atomic_json(out, {"status": "not-run", "namespace_uid": "saved-uid"})
                code = a.main([action, "--namespace", self.ns, "--context", "fixture-context", "--state", directory])
                self.assertEqual(code, 2)
                result = json.loads(out.read_text())
                self.assertEqual((result["status"], result["namespace_uid"]), ("fail", "saved-uid"))
                self.assertEqual(result["errors"][0]["type"], "KeyError")
                self.assertEqual(result["errors"][0]["stage"], action)
                self.assertNotIn("private-value", out.read_text())

    def test_main_records_both_boundary_error_and_unreadable_previous_results(self):
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(a, "execute", side_effect=KeyError("private-value")), patch("sys.stderr", io.StringIO()):
            out = Path(directory) / "results.json"
            out.write_text('{"private-value":')
            self.assertEqual(a.main(["run", "--namespace", self.ns, "--state", directory]), 2)
            result = json.loads(out.read_text())
            self.assertEqual([e["type"] for e in result["errors"]], ["JSONDecodeError", "KeyError"])
            self.assertNotIn("private-value", out.read_text())

    def test_dry_run_failure_never_writes_diagnostics_or_contacts_tools(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(a, "Kube", side_effect=AssertionError("tool access")), \
                patch("sys.stderr", io.StringIO()):
            state = Path(directory) / "absent"
            code = a.main(["run", "--namespace", self.ns, "--dry-run=server-free", "--state", str(state),
                           "--values", str(Path(directory) / "missing.json")])
            self.assertEqual(code, 2)
            self.assertFalse(state.exists())

    def test_unwritable_results_reports_safe_diagnostics_on_stderr(self):
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(a, "execute", side_effect=ValueError("private-value")), \
                patch.object(a, "atomic_json", side_effect=OSError(5, "https://example.invalid/private-value")), \
                patch("sys.stderr", io.StringIO()) as stderr:
            self.assertEqual(a.main(["run", "--namespace", self.ns, "--state", directory]), 2)
            self.assertIn("ValueError:", stderr.getvalue())
            self.assertIn("Results unavailable: OSError:", stderr.getvalue())
            self.assertNotIn("private-value", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
