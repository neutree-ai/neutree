import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parent
APP = ROOT / "v0_5_10" / "app_pd_collocated.py"
BRIDGE = ROOT.parent / "_metrics" / "sglang_ray_bridge.py"
DOCKERFILE = ROOT.parent.parent / "Dockerfile.engine-sglang"


class SGLangPDCollocatedAppStaticTest(unittest.TestCase):
    def test_pd_app_declares_sglang_disaggregation_contract(self) -> None:
        source = APP.read_text()

        self.assertIn("class PrefillActor(Backend)", source)
        self.assertIn("class DecodeActor(Backend)", source)
        self.assertIn("class PDCollocatedBackend", source)
        self.assertIn("PDRouter", source)
        self.assertIn("disaggregation_mode", source)
        self.assertIn('"prefill"', source)
        self.assertIn('"decode"', source)
        self.assertIn("disaggregation_transfer_backend", source)
        self.assertIn("bootstrap_host", source)
        self.assertIn("bootstrap_port", source)
        self.assertIn("bootstrap_room", source)
        self.assertIn("disagg_prefill_dp_rank", source)
        self.assertIn("asyncio.gather", source)
        self.assertIn('opts["name"] = _role_actor_name(', source)
        self.assertIn('"group_id": self.replica_id_str', source)
        self.assertIn('"units": units', source)
        self.assertIn('"role": "prefill"', source)
        self.assertIn('"role": "decode"', source)

    def test_sglang_pd_metrics_can_use_role_rank_env_fallback(self) -> None:
        bridge = BRIDGE.read_text()

        self.assertIn("NEUTREE_RAY_STAT_ROLE", bridge)
        self.assertIn("NEUTREE_RAY_STAT_RANK", bridge)

    def test_sglang_engine_image_contains_pd_router(self) -> None:
        dockerfile = DOCKERFILE.read_text()

        self.assertIn("COPY serve/_router ./serve/_router", dockerfile)


if __name__ == "__main__":
    unittest.main()
