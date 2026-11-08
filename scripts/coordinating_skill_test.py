"""Keep the coordinator entrypoint present and cheap to load."""
from pathlib import Path
import unittest


class CoordinatingSkillTest(unittest.TestCase):
    def test_entrypoint_fits_session_budget(self):
        skill = Path(__file__).resolve().parents[1] / "runner/skills/coordinating/SKILL.md"
        content = skill.read_text(encoding="utf-8")
        self.assertTrue(content.strip(), "coordinating skill must not be empty")
        self.assertLessEqual(len(content.splitlines()), 150, "keep the skill within 150 lines")


if __name__ == "__main__":
    unittest.main()
