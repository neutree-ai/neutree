import unittest

from router.routing import EndpointInfo
from router.runtime import RouterRuntime


class FakeServiceDiscovery:
    pass


class RouterRuntimeTests(unittest.TestCase):
    def test_pd_backend_selection_targets_sidecar_with_route_headers_and_unit_stats(self):
        runtime = RouterRuntime(FakeServiceDiscovery())
        prefill = EndpointInfo(
            url="pd://group-a/prefill/1?sidecar=http://10.0.0.1:8000",
            model_names=["m"],
            workspace="w",
            endpoint="e",
            is_pd_collocated=True,
            dispatch_url="http://10.0.0.1:8000",
            pd_role_group_id="group-a",
            pd_role="prefill",
            pd_index=1,
        )
        decode = EndpointInfo(
            url="pd://group-a/decode/2?sidecar=http://10.0.0.1:8000",
            model_names=["m"],
            workspace="w",
            endpoint="e",
            is_pd_collocated=True,
            dispatch_url="http://10.0.0.1:8000",
            pd_role_group_id="group-a",
            pd_role="decode",
            pd_index=2,
        )

        selection = runtime.select_backend([prefill, decode], {"model": "m", "prompt": "hello"})

        self.assertEqual(selection.url, "http://10.0.0.1:8000")
        self.assertEqual(
            selection.extra_headers,
            {
                "x-neutree-prefill-index": "1",
                "x-neutree-decode-index": "2",
            },
        )
        self.assertEqual(
            selection.stats_keys,
            (
                "http://10.0.0.1:8000",
                "group-a:prefill:1",
                "group-a:decode:2",
            ),
        )


if __name__ == "__main__":
    unittest.main()
