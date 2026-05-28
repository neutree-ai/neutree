import unittest

from router.scheduling import (
    ConsistentHashEndpointPicker,
    ConsistentHashWithBoundedLoadScorer,
    DomainAffinityFilter,
    EndpointInfo,
    PDSameHostProfileHandler,
    RequestStats,
    RoundRobinEndpointPicker,
    SchedulingContext,
    SchedulingProfile,
    WeightedEndpointScorer,
    WeightedScoringEndpointPicker,
)


class EndpointNameFilter:
    name = "endpoint-name"

    def __init__(self, endpoint_name):
        self._endpoint_name = endpoint_name

    def filter(self, endpoint, _context):
        return endpoint.endpoint == self._endpoint_name


class StaticScorePlugin:
    name = "static-score"

    def __init__(self, scores):
        self._scores = scores

    def score(self, endpoints, _context):
        return {
            endpoint.route_key: self._scores.get(endpoint.route_key, 0.0)
            for endpoint in endpoints
        }


def _context(stats=None, request_json=None):
    return SchedulingContext({}, stats or {}, request_json or {})


class SchedulingTests(unittest.TestCase):
    def test_round_robin_cycles_over_sorted_endpoint_urls(self):
        picker = RoundRobinEndpointPicker()
        endpoints = [
            EndpointInfo(url="http://pod-b:8000", model_names=["m"]),
            EndpointInfo(url="http://pod-a:8000", model_names=["m"]),
        ]

        got = [picker.pick(endpoints, _context(request_json={"model": "m"})).url for _ in range(4)]

        self.assertEqual(
            got,
            [
                "http://pod-a:8000",
                "http://pod-b:8000",
                "http://pod-a:8000",
                "http://pod-b:8000",
            ],
        )

    def test_consistent_hash_is_stable_for_same_chat_prefix(self):
        picker = ConsistentHashEndpointPicker()
        endpoints = [
            EndpointInfo(url="http://pod-a:8000", model_names=["m"], workspace="w", endpoint="e"),
            EndpointInfo(url="http://pod-b:8000", model_names=["m"], workspace="w", endpoint="e"),
            EndpointInfo(url="http://pod-c:8000", model_names=["m"], workspace="w", endpoint="e"),
        ]
        payload = {
            "model": "m",
            "messages": [
                {"role": "system", "content": "be concise"},
                {"role": "user", "content": "hello"},
            ],
        }

        first = picker.pick(endpoints, _context(request_json=payload))
        second = picker.pick(list(reversed(endpoints)), _context(request_json=payload))

        self.assertEqual(first.url, second.url)

    def test_consistent_hash_skips_overloaded_replica_when_possible(self):
        picker = ConsistentHashEndpointPicker(load_factor=1.0)
        endpoints = [
            EndpointInfo(url="http://pod-a:8000", model_names=["m"], workspace="w", endpoint="e"),
            EndpointInfo(url="http://pod-b:8000", model_names=["m"], workspace="w", endpoint="e"),
        ]
        stats = {
            "http://pod-a:8000": RequestStats(active_requests=50),
            "http://pod-b:8000": RequestStats(active_requests=0),
        }

        got = picker.pick(endpoints, _context(stats=stats, request_json={"model": "m", "prompt": "same"}))

        self.assertEqual(got.url, "http://pod-b:8000")

    def test_weighted_scoring_picker_combines_multiple_plugin_scores(self):
        picker = WeightedScoringEndpointPicker(
            [
                WeightedEndpointScorer(StaticScorePlugin({"pod-a": 1.0, "pod-b": 0.0}), 1.0),
                WeightedEndpointScorer(StaticScorePlugin({"pod-a": 0.0, "pod-b": 1.0}), 2.0),
            ]
        )
        endpoints = [
            EndpointInfo(id="pod-a", url="http://pod-a:8000", model_names=["m"]),
            EndpointInfo(id="pod-b", url="http://pod-b:8000", model_names=["m"]),
        ]

        got = picker.pick(endpoints, _context(request_json={"model": "m", "prompt": "same"}))

        self.assertEqual(got.url, "http://pod-b:8000")

    def test_scheduling_profile_filters_scores_and_picks_endpoint(self):
        profile = SchedulingProfile(
            [EndpointNameFilter("served")],
            [WeightedEndpointScorer(StaticScorePlugin({"http://pod-c:8000": 1.0}), 1.0)],
        )
        endpoints = [
            EndpointInfo(url="http://pod-a:8000", model_names=["m"], endpoint="ignored"),
            EndpointInfo(url="http://pod-b:8000", model_names=["m"], endpoint="served"),
            EndpointInfo(url="http://pod-c:8000", model_names=["m"], endpoint="served"),
        ]

        got = profile.pick(
            endpoints,
            SchedulingContext({}, {}, {"model": "m", "prompt": "same"}),
        )

        self.assertEqual(got.url, "http://pod-c:8000")

    def test_chwbl_scorer_falls_back_to_lowest_load_without_cache_key(self):
        profile = SchedulingProfile(
            [],
            [WeightedEndpointScorer(ConsistentHashWithBoundedLoadScorer(), 1.0)],
        )
        endpoints = [
            EndpointInfo(url="http://pod-a:8000", model_names=["m"]),
            EndpointInfo(url="http://pod-b:8000", model_names=["m"]),
        ]
        stats = {
            "http://pod-a:8000": RequestStats(active_requests=3),
            "http://pod-b:8000": RequestStats(active_requests=0),
        }

        got = profile.pick(endpoints, SchedulingContext({}, stats, {"model": "m"}))

        self.assertEqual(got.url, "http://pod-b:8000")

    def test_domain_affinity_filter_matches_selected_endpoint_domain(self):
        profile = SchedulingProfile(
            [DomainAffinityFilter()],
            [WeightedEndpointScorer(StaticScorePlugin({"prefill-a": 0.1, "prefill-b": 1.0}), 1.0)],
        )
        selected_decode = EndpointInfo(id="decode-a", url="http://decode-a:8000", model_names=["m"], domain="node-a")
        endpoints = [
            EndpointInfo(id="prefill-a", url="http://prefill-a:8000", model_names=["m"], domain="node-a"),
            EndpointInfo(id="prefill-b", url="http://prefill-b:8000", model_names=["m"], domain="node-b"),
        ]

        got = profile.pick(
            endpoints,
            SchedulingContext({}, {}, {"model": "m"}, selected_endpoint=selected_decode),
        )

        self.assertEqual(got.id, "prefill-a")

    def test_pd_profile_handler_selects_decode_endpoint_then_domain_affinity_prefill_endpoint(self):
        profile_handler = PDSameHostProfileHandler()
        prefill_a = EndpointInfo(
            url="pd://group-a/prefill/0?sidecar=http://10.0.0.1:8000",
            model_names=["m"],
            workspace="w",
            endpoint="e",
            is_pd_collocated=True,
            dispatch_url="http://10.0.0.1:8000",
            domain="group-a",
            pd_role_group_id="group-a",
            pd_role="prefill",
            pd_index=0,
        )
        decode_a = EndpointInfo(
            url="pd://group-a/decode/0?sidecar=http://10.0.0.1:8000",
            model_names=["m"],
            workspace="w",
            endpoint="e",
            is_pd_collocated=True,
            dispatch_url="http://10.0.0.1:8000",
            domain="group-a",
            pd_role_group_id="group-a",
            pd_role="decode",
            pd_index=0,
        )
        prefill_b = EndpointInfo(
            url="pd://group-b/prefill/0?sidecar=http://10.0.0.2:8000",
            model_names=["m"],
            workspace="w",
            endpoint="e",
            is_pd_collocated=True,
            dispatch_url="http://10.0.0.2:8000",
            domain="group-b",
            pd_role_group_id="group-b",
            pd_role="prefill",
            pd_index=0,
        )
        decode_b = EndpointInfo(
            url="pd://group-b/decode/0?sidecar=http://10.0.0.2:8000",
            model_names=["m"],
            workspace="w",
            endpoint="e",
            is_pd_collocated=True,
            dispatch_url="http://10.0.0.2:8000",
            domain="group-b",
            pd_role_group_id="group-b",
            pd_role="decode",
            pd_index=0,
        )
        stats = {
            decode_a.route_key: RequestStats(active_requests=20),
            decode_b.route_key: RequestStats(active_requests=0),
        }

        decision = profile_handler.pick(
            [prefill_a, decode_a, prefill_b, decode_b],
            _context(stats=stats, request_json={"model": "m", "messages": [{"role": "user", "content": "hello"}]}),
        )

        self.assertEqual(decision.endpoint, decode_b)
        self.assertEqual(decision.decode, decode_b)
        self.assertEqual(decision.prefill, prefill_b)
        self.assertEqual(decision.url, "http://10.0.0.2:8000")
        self.assertEqual(decision.prefill_index, 0)
        self.assertEqual(decision.decode_index, 0)

    def test_pd_profile_handler_falls_back_to_load_only_when_cache_key_is_missing(self):
        profile_handler = PDSameHostProfileHandler()
        endpoints = [
            EndpointInfo(
                url="pd://group-a/prefill/0?sidecar=http://10.0.0.1:8000",
                model_names=["m"],
                workspace="w",
                endpoint="e",
                is_pd_collocated=True,
                dispatch_url="http://10.0.0.1:8000",
                pd_role_group_id="group-a",
                pd_role="prefill",
                pd_index=0,
            ),
            EndpointInfo(
                url="pd://group-a/decode/0?sidecar=http://10.0.0.1:8000",
                model_names=["m"],
                workspace="w",
                endpoint="e",
                is_pd_collocated=True,
                dispatch_url="http://10.0.0.1:8000",
                pd_role_group_id="group-a",
                pd_role="decode",
                pd_index=0,
            ),
            EndpointInfo(
                url="pd://group-b/prefill/0?sidecar=http://10.0.0.2:8000",
                model_names=["m"],
                workspace="w",
                endpoint="e",
                is_pd_collocated=True,
                dispatch_url="http://10.0.0.2:8000",
                pd_role_group_id="group-b",
                pd_role="prefill",
                pd_index=0,
            ),
            EndpointInfo(
                url="pd://group-b/decode/0?sidecar=http://10.0.0.2:8000",
                model_names=["m"],
                workspace="w",
                endpoint="e",
                is_pd_collocated=True,
                dispatch_url="http://10.0.0.2:8000",
                pd_role_group_id="group-b",
                pd_role="decode",
                pd_index=0,
            ),
        ]
        stats = {
            "group-a:decode:0": RequestStats(active_requests=3),
            "group-b:decode:0": RequestStats(active_requests=0),
        }

        decision = profile_handler.pick(endpoints, _context(stats=stats, request_json={"model": "m"}))

        self.assertEqual(decision.endpoint.route_key, "group-b:decode:0")
        self.assertEqual(decision.prefill.route_key, "group-b:prefill:0")

    def test_pd_profile_handler_fails_closed_without_local_prefill(self):
        profile_handler = PDSameHostProfileHandler()
        endpoint = EndpointInfo(
            url="pd://group-a/decode/0?sidecar=http://10.0.0.1:8000",
            model_names=["m"],
            workspace="w",
            endpoint="e",
            is_pd_collocated=True,
            dispatch_url="http://10.0.0.1:8000",
            pd_role_group_id="group-a",
            pd_role="decode",
            pd_index=0,
        )

        with self.assertRaisesRegex(ValueError, "no ready prefill endpoint"):
            profile_handler.pick([endpoint], _context(request_json={"model": "m"}))


if __name__ == "__main__":
    unittest.main()
