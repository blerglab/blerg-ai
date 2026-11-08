import contextlib
import importlib.machinery
import importlib.util
import io
from pathlib import Path
import unittest
from unittest.mock import patch

loader = importlib.machinery.SourceFileLoader("blerg_metadata_cli", str(Path(__file__).with_name("blerg-runner")))
spec = importlib.util.spec_from_loader(loader.name, loader)
cli = importlib.util.module_from_spec(spec)
loader.exec_module(cli)

class BoardMetadataCLI(unittest.TestCase):
    def test_patch_preserves_empty_description_and_omits_unspecified_fields(self):
        with patch.object(cli, "SESSION_ID", "fixture"), patch.object(cli, "_post", return_value={"proposal_id": "proposal"}) as post, contextlib.redirect_stdout(io.StringIO()) as out:
            cli.cmd_board_update("Board", description="")
        post.assert_called_once_with("/api/sessions/fixture/board-metadata", {"board": "Board", "description": ""})
        self.assertIn("do not propose it again", out.getvalue())

    def test_no_fields_refused(self):
        with patch.object(cli, "_post") as post, contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            cli.cmd_board_update("Board")
        post.assert_not_called()

    def test_configuration_patch_preserves_false_and_empty_lists(self):
        with patch.object(cli, "SESSION_ID", "fixture"), patch.object(cli, "_post", return_value={"proposal_id": "proposal"}) as post, contextlib.redirect_stdout(io.StringIO()):
            cli.cmd_board_update("Board", patch='{"repos":[],"require_repo":false,"tags":[{"from":"old","delete":true}]}')
        post.assert_called_once_with("/api/sessions/fixture/board-metadata", {"board":"Board","repos":[],"require_repo":False,"tags":[{"from":"old","delete":True}]})
