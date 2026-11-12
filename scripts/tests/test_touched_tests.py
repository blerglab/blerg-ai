from pathlib import Path
import sys
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import tempfile
import unittest
import touched_tests as touched

class TouchedTest(unittest.TestCase):
    def packages(self):
        return [
            dict(module='contracts', relative_dir='contracts/wire', ImportPath='example.com/contracts/wire'),
            dict(module='runner', relative_dir='runner/internal/a', ImportPath='example.com/runner/a', Imports=['example.com/contracts/wire']),
            dict(module='runner', relative_dir='runner/internal/b', ImportPath='example.com/runner/b', TestImports=['example.com/runner/a']),
            dict(module='runner', relative_dir='runner/internal/c', ImportPath='example.com/runner/c', XTestImports=['example.com/runner/b']),
            dict(module='runner', relative_dir='runner/internal/other', ImportPath='example.com/runner/other'),
        ]

    def test_cross_module_transitive_and_test_importers(self):
        got = touched.affected_packages(['contracts/wire/event.go'], self.packages())
        self.assertEqual(got['contracts'], ['./wire'])
        self.assertEqual(got['runner'], ['./internal/a', './internal/b', './internal/c'])

    def test_embedded_fixture_marks_nearest_package(self):
        got = touched.affected_packages(['runner/internal/b/testdata/fixture.json'], self.packages())
        self.assertEqual(got['runner'], ['./internal/b', './internal/c'])

    def test_manifest_and_deleted_package_fall_back_to_module(self):
        for file in ('runner/go.mod', 'runner/go.sum', 'runner/deleted/a.go'):
            self.assertEqual(len(touched.affected_packages([file], self.packages())['runner']), 4)

    def test_json_stream(self):
        self.assertEqual(list(touched.json_objects(' {"a":1}\n {"b":2}\n')), [{'a':1}, {'b':2}])

    def test_changes_include_rename_deletion_working_tree_and_untracked(self):
        import subprocess
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            def git(*args):
                return subprocess.check_output(['git', *args], cwd=root, text=True)
            git('init', '-q')
            git('config', 'user.email', 'agent@example.com')
            git('config', 'user.name', 'Agent')
            (root/'old.go').write_text('old')
            (root/'delete.go').write_text('delete')
            git('add', '.')
            git('commit', '-qm', 'baseline')
            git('mv', 'old.go', 'renamed.go')
            (root/'delete.go').unlink()
            (root/'untracked.ts').touch()
            self.assertEqual(touched.changed_files(root, 'HEAD'),
                             ['delete.go', 'old.go', 'renamed.go', 'untracked.ts'])

    def test_runner_package_without_helpers_does_not_require_tracing(self):
        suites = [('host-tracing-preflight', '.', ['probe']),
                  ('runner-go', 'runner', ['go', 'test', './...'])]
        selected = touched.select(suites, Path('.'), ['runner/internal/other/a.go'], self.packages())
        self.assertEqual([name for name, _, _ in selected], ['runner-go'])

    def test_module_installer_changes_retain_script_checks(self):
        suites = [('runner-go', 'runner', ['go', 'test', './...']),
                  ('scripts', '.', ['make', 'check-other-scripts'])]
        selected = touched.select(suites, Path('.'), ['runner/daemon/install.sh'], self.packages())
        self.assertEqual([name for name, _, _ in selected], ['runner-go', 'scripts'])

    def test_unknown_root_input_keeps_all_ordinary_tests(self):
        suites = [('contracts-go', 'contracts', ['go', 'test', './...']),
                  ('runner-unit', 'runner/frontend', ['npm', 'test', '--', '--project', 'unit']),
                  ('runner-browser', 'runner/frontend', ['npm', 'test', '--', '--project', 'browser']),
                  ('runner-performance', 'runner/frontend', ['npm', 'run', 'test:performance']),
                  ('daemon-race', 'runner', ['go', 'test', '-race']),
                  ('scripts', '.', ['make', 'check-other-scripts'])]
        selected = touched.select(suites, Path('.'), ['Makefile'], self.packages())
        self.assertEqual([name for name, _, _ in selected],
                         ['contracts-go', 'runner-unit', 'runner-browser', 'runner-performance', 'scripts'])

    def test_selection(self):
        suites = [('runner-go','runner',['go','test','./...']), ('contracts-go','contracts',['go','test','./...']),
                  ('runner-go-lint','runner',['lint','./...']), ('runner-unit','runner/frontend',['npm','test','--','--project','unit']),
                  ('runner-browser','runner/frontend',['npm','test','--','--project','browser']), ('runner-performance','runner/frontend',['npm','run','test:performance']),
                  ('chat-tests','packages/chat',['npm','test','--','--maxWorkers=2']), ('board-tests','board/web',['npm','test','--','--maxWorkers=2']),
                  ('runner-types','runner/frontend',['tsc']), ('scripts','.', ['make','check-other-scripts'])]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            file = root/'packages/chat/src/model.ts'
            file.parent.mkdir(parents=True); file.touch()
            got = {name: cmd for name, _, cmd in touched.select(suites, root, ['packages/chat/src/model.ts'], self.packages())}
            self.assertEqual(set(got), {'runner-unit','runner-browser','runner-performance','runner-types','chat-tests','board-tests'})
            self.assertEqual(got['runner-unit'][:6], ['npx','--no-install','vitest','related','--run','--passWithNoTests'])
            self.assertIn(str(file), got['runner-unit'])
            # A deleted source cannot be resolved, so run all tests in consumers.
            file.unlink()
            deleted = dict((name,cmd) for name,_,cmd in touched.select(suites, root, ['packages/chat/src/model.ts'], self.packages()))
            self.assertEqual(deleted['runner-unit'][:2], ['npm','test'])
            go = dict((name,cmd) for name,_,cmd in touched.select(suites, root, ['contracts/wire/types.go'], self.packages()))
            self.assertEqual(go['runner-go'], ['go','test','./internal/a','./internal/b','./internal/c'])
            self.assertEqual(go['runner-go-lint'], ['lint','./...'])

if __name__ == '__main__': unittest.main()
