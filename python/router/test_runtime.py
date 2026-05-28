import unittest

from router.routing import EndpointInfo, PDRouteUnit
from router.runtime import RouterRuntime


class FakeServiceDiscovery:
    pass


class RouterRuntimeTests(unittest.TestCase):
    def test_pd_backend_selection_targets_sidecar_with_route_headers_and_unit_stats(self):
        runtime = RouterRuntime(FakeServiceDiscovery())
        endpoint = EndpointInfo(
            url="http://10.0.0.1:8000",
            model_names=["m"],
            workspace="w",
            endpoint="e",
            is_pd_collocated=True,
            pd_route_units=[
                PDRouteUnit("group-a", "prefill", 1, True, "http://10.0.0.1:8000"),
                PDRouteUnit("group-a", "decode", 2, True, "http://10.0.0.1:8000"),
            ],
        )

        selection = runtime.select_backend([endpoint], {"model": "m", "prompt": "hello"})

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
