import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parent
APP_FILES = [
    ROOT / "v0_17_1" / "app_pd_collocated.py",
    ROOT / "v0_20_0" / "app_pd_collocated.py",
]


class PDCollocatedAppStaticTest(unittest.TestCase):
    def test_role_actors_set_deterministic_names(self) -> None:
        for path in APP_FILES:
            with self.subTest(path=path):
                source = path.read_text()
                self.assertIn("def _role_actor_name", source)
                self.assertIn('opts["name"] = _role_actor_name(', source)
                self.assertIn('"prefill", rank', source)
                self.assertIn('"decode", rank', source)


if __name__ == "__main__":
    unittest.main()
