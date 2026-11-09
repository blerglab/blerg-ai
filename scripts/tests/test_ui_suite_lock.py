import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from ui_suite_lock import ui_suite_lock

class UILockTests(unittest.TestCase):
    def test_creates_lock_and_nested_worker_can_reuse_it(self):
        with tempfile.TemporaryDirectory() as tmp:
            path=Path(tmp)/'new'/'ui.lock'
            code='from ui_suite_lock import ui_suite_lock; import sys;\nwith ui_suite_lock(path=sys.argv[1]): print("nested")'
            with ui_suite_lock(path=path) as fd:
                result=subprocess.run([sys.executable,'-c',code,str(path)], env=dict(os.environ,PYTHONPATH=str(Path(__file__).resolve().parents[1])),pass_fds=(fd,),capture_output=True,text=True,timeout=5)
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertTrue(path.exists())
    def test_external_flock_does_not_deadlock(self):
        with tempfile.TemporaryDirectory() as tmp:
            path=Path(tmp)/'ui.lock'
            code='from ui_suite_lock import ui_suite_lock; import sys;\nwith ui_suite_lock(path=sys.argv[1]): print("nested")'
            result=subprocess.run(['flock',str(path),sys.executable,'-c',code,str(path)],env=dict(os.environ,PYTHONPATH=str(Path(__file__).resolve().parents[1])),capture_output=True,text=True,timeout=5)
            self.assertEqual(result.returncode,0,result.stderr)

    def test_default_lock_creates_a_fresh_cache_tree(self):
        with tempfile.TemporaryDirectory() as tmp, patch.dict(os.environ,{'XDG_CACHE_HOME':str(Path(tmp)/'new-cache')}):
            os.environ.pop('BLERG_UI_SUITE_LOCK', None)
            with ui_suite_lock():
                self.assertEqual(len(list((Path(tmp)/'new-cache').rglob('ui-suite.lock'))),1)

    def test_inherited_lock_without_proc_filesystem(self):
        original = Path.iterdir
        def list_fds(path):
            if path == Path('/proc/self/fd'):
                raise FileNotFoundError('no proc filesystem')
            return original(path)
        with tempfile.TemporaryDirectory() as tmp, patch.object(Path, 'iterdir', list_fds):
            path = Path(tmp) / 'ui.lock'
            with ui_suite_lock(path=path) as outer:
                with ui_suite_lock(path=path) as nested:
                    self.assertEqual(nested, outer)
