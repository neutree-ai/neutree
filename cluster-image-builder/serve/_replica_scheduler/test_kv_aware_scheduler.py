import asyncio
import importlib.util
import sys
import time
import types
import unittest
from pathlib import Path
from unittest.mock import patch

from serve._kv_cache import (
    KV_ROUTING_METADATA_KEY,
    KV_ROUTING_STATS_KEY,
    token_block_fingerprints,
)


class _RequestRouter:
    def __init__(self, *args, **kwargs):
        pass


def _load_scheduler_class():
    modules = {
        "ray": types.ModuleType("ray"),
        "ray.serve": types.ModuleType("ray.serve"),
        "ray.serve._private": types.ModuleType("ray.serve._private"),
        "ray.serve._private.constants": types.ModuleType(
            "ray.serve._private.constants"
        ),
        "ray.serve._private.request_router": types.ModuleType(
            "ray.serve._private.request_router"
        ),
        "ray.serve._private.request_router.common": types.ModuleType(
            "ray.serve._private.request_router.common"
        ),
        "ray.serve._private.request_router.replica_wrapper": types.ModuleType(
            "ray.serve._private.request_router.replica_wrapper"
        ),
        "ray.serve._private.request_router.request_router": types.ModuleType(
            "ray.serve._private.request_router.request_router"
        ),
    }
    modules["ray.serve._private.constants"].SERVE_LOGGER_NAME = "ray.serve"
    modules["ray.serve._private.request_router.common"].PendingRequest = object
    modules["ray.serve._private.request_router.replica_wrapper"].RunningReplica = object
    modules["ray.serve._private.request_router.request_router"].RequestRouter = _RequestRouter

    path = Path(__file__).with_name("kv_aware_scheduler.py")
    spec = importlib.util.spec_from_file_location("kv_aware_scheduler_under_test", path)
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, modules):
        spec.loader.exec_module(module)
    return module.KVAwareReplicaScheduler


KVAwareReplicaScheduler = _load_scheduler_class()


class _Replica:
    def __init__(self, replica_id, stats):
        self.replica_id = replica_id
        self.routing_stats = stats


def _stats(tokens, block_size, matched_blocks, *, age_s=0.0):
    fingerprints = token_block_fingerprints(tokens, block_size)[:matched_blocks]
    return {
        KV_ROUTING_STATS_KEY: {
            "version": 1,
            "ready": True,
            "trusted": True,
            "consumer_alive": True,
            "captured_at": time.time() - age_s,
            "block_size": block_size,
            "block_fingerprints": fingerprints,
        }
    }


class TestKVAwareReplicaScheduler(unittest.TestCase):
    def setUp(self):
        self.scheduler = KVAwareReplicaScheduler()
        self.scheduler.initialize_state(max_index_age_s=5.0, min_matched_blocks=1)
        self.tokens = list(range(1, 9))
        self.pending_request = types.SimpleNamespace(
            args=(
                {
                    KV_ROUTING_METADATA_KEY: {
                        "version": 1,
                        "token_ids": self.tokens,
                    }
                },
            )
        )

    def test_prefers_replica_with_longest_resident_prefix(self):
        first = _Replica("first", _stats(self.tokens, 2, 1))
        second = _Replica("second", _stats(self.tokens, 2, 3))

        ranks = asyncio.run(
            self.scheduler.choose_replicas([first, second], self.pending_request)
        )
        self.assertEqual(ranks, [[second], [first]])

    def test_tied_best_replicas_share_first_rank(self):
        first = _Replica("first", _stats(self.tokens, 2, 2))
        second = _Replica("second", _stats(self.tokens, 2, 2))

        ranks = asyncio.run(
            self.scheduler.choose_replicas([first, second], self.pending_request)
        )
        self.assertEqual(ranks, [[first, second]])

    def test_missing_metadata_falls_back_to_all_candidates(self):
        replicas = [
            _Replica("first", _stats(self.tokens, 2, 3)),
            _Replica("second", _stats(self.tokens, 2, 0)),
        ]
        pending = types.SimpleNamespace(args=({"messages": []},))
        ranks = asyncio.run(self.scheduler.choose_replicas(replicas, pending))
        self.assertEqual(ranks, [replicas])

    def test_stale_stats_fall_back_to_all_candidates(self):
        replicas = [
            _Replica("first", _stats(self.tokens, 2, 3, age_s=10.0)),
            _Replica("second", _stats(self.tokens, 2, 0)),
        ]
        ranks = asyncio.run(
            self.scheduler.choose_replicas(replicas, self.pending_request)
        )
        self.assertEqual(ranks, [replicas])


if __name__ == "__main__":
    unittest.main()
