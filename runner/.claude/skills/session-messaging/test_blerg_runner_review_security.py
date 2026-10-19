"""Independent hostile-input checks for descriptive diff review metadata."""

import os
from pathlib import Path
import runpy
import unittest
from unittest import mock


class TestDiffAnchorSecurity(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # Loading the CLI must not read this test process's live session identity.
        with mock.patch.dict(os.environ, {}, clear=True):
            cls.cli = runpy.run_path(str(Path(__file__).with_name("blerg-runner")),
                                    run_name="review_security_fixture")

    def anchor(self, **overrides):
        return {
            "file": "src/example.ts", "side": "new", "start": 11, "end": 13,
            "hunk": 2, "fileIndex": 3, "commit": "ABCDEF01" * 5, **overrides,
        }

    def test_complete_identity_is_printed_with_canonical_commit(self):
        label = self.cli["_review_diff_where"](self.anchor())
        self.assertEqual(label, "src/example.ts, new lines 11–13, hunk 2, file 3, commit abcdef01abcd")
        self.assertIn("old line 11", self.cli["_review_diff_where"](self.anchor(side="old", end=11)))

    def test_filename_is_only_data_even_when_it_looks_like_a_command_or_url(self):
        name = "../$(touch marker);`echo no` [link](https://example.com).ts"
        with mock.patch("builtins.open", side_effect=AssertionError("opened a path")), \
                mock.patch("subprocess.run", side_effect=AssertionError("ran a command")), \
                mock.patch("os.system", side_effect=AssertionError("ran a shell")), \
                mock.patch("urllib.request.urlopen", side_effect=AssertionError("opened a URL")):
            label = self.cli["_review_diff_where"](self.anchor(file=name))
        self.assertTrue(label.startswith(name + ", new"))

    def test_control_characters_cannot_reach_terminal_output_via_diff_paths(self):
        for character in map(chr, [*range(32), *range(127, 160)]):
            with self.subTest(character=repr(character)):
                value = self.anchor(file="src/" + character + "example.ts")
                self.assertIsNone(self.cli["_review_diff_where"](value))
                self.assertEqual(self.cli["_review_where"]({"kind": "text", "diff": value}), "(no place)")

    def test_invalid_coordinates_include_booleans_fractions_and_unsafe_integers(self):
        for key in ("start", "end", "hunk", "fileIndex"):
            for bad in (True, False, 0, -1, 1.5, "1", None, float("nan"), float("inf"), 2 ** 53):
                with self.subTest(key=key, value=bad):
                    self.assertIsNone(self.cli["_review_diff_where"](self.anchor(**{key: bad})))

    def test_reversed_and_excessive_ranges_are_not_printed_as_valid_targets(self):
        self.assertIsNone(self.cli["_review_diff_where"](self.anchor(start=14, end=13)))
        self.assertIsNone(self.cli["_review_diff_where"](self.anchor(start=1, end=201)))

    def test_malformed_commit_side_and_path_metadata_are_rejected(self):
        for overrides in (
                {"commit": "abc1234"}, {"commit": "g" * 40}, {"commit": "a" * 65},
                {"commit": 123}, {"side": "both"}, {"side": "NEW"}, {"side": None},
                {"file": ""}, {"file": "   "}, {"file": 123}, {"file": "x" * 4097}):
            with self.subTest(overrides=overrides):
                self.assertIsNone(self.cli["_review_diff_where"](self.anchor(**overrides)))

    def test_full_sha256_is_accepted_but_unknown_keys_do_not_affect_output(self):
        value = self.anchor(commit="a" * 64)
        expected = self.cli["_review_diff_where"](value)
        self.assertIn("commit aaaaaaaaaaaa", expected)
        value.update(url="https://example.com/track", command="do not run")
        self.assertEqual(self.cli["_review_diff_where"](value), expected)


if __name__ == "__main__":
    unittest.main()
