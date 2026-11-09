import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("converter", Path(__file__).parents[1] / "convert-review-rounds.py")
converter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(converter)

class ReviewConversion(unittest.TestCase):
    def test_preserves_counts_and_rounds(self):
        row = dict(at="2026-10-01T12:00:00Z",summary="Review",engine="codex",target="abcdef1",round="2",verdict="changes",high="2",medium="1",low="0")
        got = converter.convert([row],{},"board-a",368)[0]
        self.assertEqual(got["findings"],["high","high","medium"])
        self.assertEqual(got["round"],2)
        with self.assertRaises(ValueError):
            converter.convert([{**row,"target":""}],{},"board-a",368)

    def test_missing_severity_columns_are_unknown_not_zero(self):
        row = dict(at="2026-10-01T12:00:00Z", summary="Review", engine="codex", target="abcdef1", round="1", verdict="approved")
        with self.assertRaises(ValueError):
            converter.convert([row], {}, "board-a", 368)

    def test_date_only_audit_preserves_unknowns_and_source_labels(self):
        row=dict(date="2026-10-31",card="BLG-321-tab",round="2",target="abcdef1",builder="codex",reviewer="claude(main)",verdict="changes",high="2",medium="0",low="1",notes="Review")
        got=converter.convert_audit([row])[0]
        self.assertIsNone(got['at'])
        self.assertIsNone(got['card'])
        self.assertIsNone(got['board_id'])
        self.assertEqual(got['source'],row)
        self.assertEqual(got['review_date'],"2026-10-31")
        self.assertEqual(got['reviewer_label'],"claude(main)")
        self.assertEqual(got['findings'],["high","high","low"])
        self.assertFalse(got['import_ready'])
