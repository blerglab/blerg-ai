from pathlib import Path
import json
import sys
import tempfile
import unittest
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import vitest_results_cache as cache


class VitestResultsCacheTests(unittest.TestCase):
    def test_invalid_row_values_never_reach_a_worktree_cache(self):
        for row in ({'duration': 'slow', 'failed': False}, {'duration': 1, 'failed': None}):
            with self.subTest(row=row), tempfile.TemporaryDirectory() as directory:
                shared = Path(directory)/'shared'
                path = shared/'.cache/vite-chat/vitest/project/results.json'
                path.parent.mkdir(parents=True)
                path.write_text(json.dumps({'version': '4.1.8', 'results': [['unit:file', row]]}))
                root = Path(directory)/'checkout'
                with self.assertRaises(ValueError):
                    cache.sync(root, 'packages/chat', restore=True, shared=shared)
                self.assertFalse(root.exists())

    def test_shares_only_scheduling_hints_and_retains_checkout_local_results(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)/'checkout'
            shared = Path(directory)/'shared'
            relative = Path('.cache/vite-runner/vitest/project/results.json')
            results = {'version': '4.1.8', 'results': [['unit:src/a.test.ts', {'duration': 42, 'failed': False}]]}
            source = root/relative
            source.parent.mkdir(parents=True)
            source.write_text(json.dumps(results))
            (source.parent/'transformed.js').write_text('private source')
            cache.sync(root, 'runner/frontend', restore=False, shared=shared)
            self.assertEqual([p for p in shared.rglob('*') if p.is_file()], [shared/relative])
            destination = Path(directory)/'second-checkout'
            cache.sync(destination, 'runner/frontend', restore=True, shared=shared)
            self.assertEqual(json.loads((destination/relative).read_text()), results)
            (destination/relative).write_text('local results win')
            cache.sync(destination, 'runner/frontend', restore=True, shared=shared)
            self.assertEqual((destination/relative).read_text(), 'local results win')

    def test_invalid_shared_metadata_is_rejected_before_vitest_reads_it(self):
        with tempfile.TemporaryDirectory() as directory:
            shared = Path(directory)/'shared'
            path = shared/'.cache/vite-chat/vitest/project/results.json'
            path.parent.mkdir(parents=True)
            path.write_text('{broken')
            root = Path(directory)/'checkout'
            with self.assertRaises(ValueError):
                cache.sync(root, 'packages/chat', restore=True, shared=shared)
            self.assertFalse(root.exists())

    def test_unknown_app_does_not_read_or_write_source(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            cache.sync(root, 'unknown', restore=True, shared=root/'shared')
            self.assertEqual(list(root.iterdir()), [])
