"""Timeline rules of the assembly: waits are compressed and labeled, the rest plays in real time.

    python3 -m unittest discover -s video/demo -p 'test_*.py'  (and -s video/build)
"""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
try:
    import short  # noqa: E402
except ImportError as exc:  # numpy, opencv, Pillow or PyYAML missing: nothing to test here
    raise unittest.SkipTest(f"assembly dependencies missing: {exc}")


class Spec:
    def __init__(self, d):
        self.d = d


TAKE = {"marks": {"a": 2.0, "b": 30.0}, "waits": [{"label": "agent", "start": 5.0, "end": 25.0}]}


class PiecesTest(unittest.TestCase):
    def test_a_wait_plays_in_wait_seconds_and_the_rest_in_real_time(self):
        ps = short.pieces(Spec({"wait_seconds": 4.0}), {"from": "a", "to": "b"}, TAKE)
        self.assertEqual([(a, b, label) for a, b, _, label in ps], [(2.0, 5.0, None), (5.0, 25.0, "wait"), (25.0, 30.0, None)])
        self.assertEqual([s for _, _, s, _ in ps], [1.0, 5.0, 1.0])
        self.assertAlmostEqual(sum((b - a) / s for a, b, s, _ in ps), 3 + 4 + 5)

    def test_a_short_wait_is_never_slowed_down(self):
        take = {"marks": {"a": 0.0, "b": 4.0}, "waits": [{"label": "x", "start": 1.0, "end": 2.0}]}
        ps = short.pieces(Spec({"wait_seconds": 3.0}), {"from": "a", "to": "b"}, take)
        self.assertTrue(all(s >= 1.0 for _, _, s, _ in ps))

    def test_source_time_holds_the_last_frame_past_the_footage(self):
        item = {"pieces": [(2.0, 5.0, 1.0, None), (5.0, 25.0, 5.0, "wait")]}
        self.assertAlmostEqual(short.src_time(item, 1.0)[0], 3.0)
        t, speed, label = short.src_time(item, 4.0)
        self.assertAlmostEqual(t, 10.0)
        self.assertEqual((speed, label), (5.0, "wait"))
        self.assertAlmostEqual(short.src_time(item, 60.0)[0], 24.95)


class ElevenLabsTest(unittest.TestCase):
    def test_voice_id_is_escaped_and_a_failed_download_leaves_no_file(self):
        import tempfile
        import urllib.request
        from unittest import mock
        seen = {}

        def fail(req, timeout):
            seen["url"] = req.full_url
            raise OSError("network down")

        spec = Spec({"voice": {"provider": "elevenlabs", "voice_id": "a/../b?x"}})
        spec.scenes = [{}]
        with tempfile.TemporaryDirectory() as out, \
                mock.patch.dict(os.environ, {"ELEVENLABS_API_KEY": "k"}), \
                mock.patch.object(short, "OUT", out), mock.patch.object(urllib.request, "urlopen", fail):
            with self.assertRaises(OSError):
                short.tts(spec, "Hello.", 0, silent=False)
            self.assertEqual(os.listdir(os.path.join(out, "cache", "vo")), [])
        self.assertTrue(seen["url"].endswith("/text-to-speech/a%2F..%2Fb%3Fx"), seen["url"])


class CaptionsTest(unittest.TestCase):
    def test_srt_and_ass_times(self):
        self.assertEqual(short.srt_time(61.5), "00:01:01,500")
        self.assertEqual(short.ass_time(61.5), "0:01:01.50")


if __name__ == "__main__":
    unittest.main()
