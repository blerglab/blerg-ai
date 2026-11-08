import os
import runpy
import sys
import unittest
from unittest import mock

class WorkspaceMetricsDispatchTest(unittest.TestCase):
    def test_test_report_uses_the_installed_go_runtime(self):
        namespace = runpy.run_path(os.path.join(os.path.dirname(__file__), "blerg-runner"))
        with mock.patch.dict(os.environ, {"BLERG_RUNNER_METRICS_BIN": "/missing/runtime"}), mock.patch.object(sys, "argv", ["blerg-runner", "test", "report", "--help"]), mock.patch("shutil.which", return_value="/runtime/blerg-workspace-metrics"), mock.patch("os.execv", side_effect=SystemExit(0)) as execute:
            with self.assertRaises(SystemExit):
                namespace["main"]()
            self.assertEqual(execute.call_args.args if execute.called else None, ("/runtime/blerg-workspace-metrics", ["/runtime/blerg-workspace-metrics", "test", "report", "--help"]))

    def test_missing_host_binary_uses_sandbox_helper(self):
        namespace = runpy.run_path(os.path.join(os.path.dirname(__file__), "blerg-runner"))
        def which(name):
            return "/runtime/blerg-workspace-metrics" if name == "blerg-workspace-metrics" else None
        with mock.patch.dict(os.environ, {"BLERG_RUNNER_METRICS_BIN": "/missing/host-binary"}), mock.patch.object(sys, "argv", ["blerg-runner", "workspace", "run", "--help"]), mock.patch("shutil.which", side_effect=which), mock.patch("os.execv", side_effect=RuntimeError("dispatched")) as execute:
            with self.assertRaisesRegex(RuntimeError, "dispatched"):
                namespace["main"]()
            self.assertEqual(execute.call_args.args, ("/runtime/blerg-workspace-metrics", ["/runtime/blerg-workspace-metrics", "workspace", "run", "--help"]))

    def test_plan_uses_the_installed_go_runtime(self):
        namespace = runpy.run_path(os.path.join(os.path.dirname(__file__), "blerg-runner"))
        with mock.patch.dict(os.environ, {"BLERG_RUNNER_METRICS_BIN": "/missing/runtime"}), mock.patch.object(sys, "argv", ["blerg-runner", "plan", "show"]), mock.patch("shutil.which", return_value="/runtime/blerg-workspace-metrics"), mock.patch("os.execv", side_effect=RuntimeError("dispatched")) as execute:
            with self.assertRaisesRegex(RuntimeError, "dispatched"):
                namespace["main"]()
            self.assertEqual(execute.call_args.args, ("/runtime/blerg-workspace-metrics", ["/runtime/blerg-workspace-metrics", "plan", "show"]))
