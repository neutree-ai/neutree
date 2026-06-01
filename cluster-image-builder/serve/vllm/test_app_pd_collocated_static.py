import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parent
APP_FILES = [
    ROOT / "v0_20_0" / "app_pd_collocated.py",
]
BACKEND_FILES = [
    ROOT / "v0_20_0" / "app.py",
]
ROUTER_FILES = [
    ROOT.parent / "_router" / "pd_router.py",
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
                self.assertIn('"group_id": self.replica_id_str', source)
                self.assertIn('"units": units', source)
                self.assertIn('"role": "prefill"', source)
                self.assertIn('"role": "decode"', source)

    def test_decode_actor_caps_completion_limits_to_model_len(self) -> None:
        for path in APP_FILES:
            with self.subTest(path=path):
                source = path.read_text()
                self.assertIn("def _model_max_token_limit", source)
                self.assertIn("def _clamp_completion_limits", source)
                self.assertIn('for key in ("max_tokens", "max_completion_tokens")', source)
                self.assertIn("def _normalize_decode_payload", source)
                self.assertIn("return await super().generate(self._normalize_decode_payload(payload))", source)
                self.assertIn("[pd_generate][decode_limit_clamped]", source)

    def test_backend_returns_vllm_validation_errors(self) -> None:
        for path in BACKEND_FILES:
            with self.subTest(path=path):
                source = path.read_text()
                self.assertIn("from vllm.exceptions import VLLMValidationError", source)
                self.assertIn("except VLLMValidationError as e:", source)
                self.assertIn('"type": "invalid_request_error"', source)
                self.assertIn('"code": 400', source)

    def test_pd_router_honors_structured_error_status_code(self) -> None:
        for path in ROUTER_FILES:
            with self.subTest(path=path):
                source = path.read_text()
                self.assertIn("def _error_status_code", source)
                self.assertIn("400 <= code < 600", source)
                self.assertIn("status_code=_error_status_code(result)", source)

    def test_pd_router_can_refresh_empty_topology_on_request_path(self) -> None:
        for path in ROUTER_FILES:
            with self.subTest(path=path):
                source = path.read_text()
                self.assertIn("async def _ensure_topology_ready", source)
                self.assertIn("await self._ensure_topology_ready()", source)
                self.assertIn("async def _pull_any_topology", source)
                self.assertIn("self._upsert_topology_dict(topo_dict, requested_replica_id=\"\")", source)
                self.assertIn("def _upsert_topology_dict", source)


if __name__ == "__main__":
    unittest.main()
