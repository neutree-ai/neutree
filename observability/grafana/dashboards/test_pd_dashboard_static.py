import json
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parent
DASHBOARDS = [
    ROOT / "vllm_grafana_dashboard.json",
    ROOT / "sglang_grafana_dashboard.json",
]


class PDDashboardStaticTest(unittest.TestCase):
    def test_dashboards_include_pd_role_rank_and_kv_panels(self) -> None:
        for path in DASHBOARDS:
            with self.subTest(path=path.name):
                dashboard = json.loads(path.read_text())
                variables = {
                    item.get("name")
                    for item in dashboard.get("templating", {}).get("list", [])
                }
                self.assertIn("Role", variables)
                self.assertIn("Rank", variables)

                titles = {
                    panel.get("title")
                    for panel in dashboard.get("panels", [])
                }
                self.assertIn("P/D Role Health", titles)
                self.assertIn("P/D KV Cache", titles)
                self.assertIn("P/D KV Transfer", titles)

                exprs = [
                    target.get("expr", "")
                    for panel in dashboard.get("panels", [])
                    for target in panel.get("targets", [])
                ]
                self.assertTrue(
                    any('role=~"$Role"' in expr for expr in exprs),
                    "expected at least one query filtered by role",
                )
                self.assertTrue(
                    any('rank=~"$Rank"' in expr for expr in exprs),
                    "expected at least one query filtered by rank",
                )


if __name__ == "__main__":
    unittest.main()
