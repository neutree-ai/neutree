import unittest
from dataclasses import dataclass, field
from typing import Any, Optional

from serve._kv_cache.local_index import (
    KV_ROUTING_STATS_KEY,
    LocalKVBlockIndex,
    build_local_kv_events_config,
    build_native_cpu_offload_config,
    match_prefix_blocks,
    token_block_fingerprints,
)


@dataclass
class BlockStored:
    block_hashes: list[Any]
    parent_block_hash: Any
    token_ids: list[int]
    block_size: int
    medium: Optional[str] = "GPU"


@dataclass
class BlockRemoved:
    block_hashes: list[Any]
    medium: Optional[str] = "GPU"


class AllBlocksCleared:
    pass


@dataclass
class Batch:
    events: list[Any] = field(default_factory=list)


def _routing_stats(index: LocalKVBlockIndex, *, now: float = 100.0):
    snapshot = index.snapshot()
    snapshot.update({"captured_at": now, "consumer_alive": True})
    return {KV_ROUTING_STATS_KEY: snapshot}


class TestLocalKVBlockIndex(unittest.TestCase):
    def test_store_builds_the_same_token_chain_as_router(self):
        index = LocalKVBlockIndex()
        tokens = list(range(1, 9))
        index.apply_batch(
            0,
            Batch([BlockStored([b"h1", b"h2"], None, tokens, 4)]),
        )

        snapshot = index.snapshot()
        self.assertTrue(snapshot["ready"])
        self.assertTrue(snapshot["trusted"])
        self.assertEqual(snapshot["block_size"], 4)
        self.assertEqual(snapshot["block_count"], 2)
        self.assertCountEqual(
            snapshot["block_fingerprints"],
            token_block_fingerprints(tokens, 4),
        )
        self.assertEqual(match_prefix_blocks(tokens, _routing_stats(index), now=100.0), 2)

    def test_child_event_uses_parent_chain(self):
        index = LocalKVBlockIndex()
        index.apply_batch(0, Batch([BlockStored([b"h1"], None, [1, 2], 2)]))
        index.apply_batch(1, Batch([BlockStored([b"h2"], b"h1", [3, 4], 2)]))

        tokens = [1, 2, 3, 4]
        self.assertEqual(match_prefix_blocks(tokens, _routing_stats(index), now=100.0), 2)

    def test_remove_respects_duplicate_hash_references(self):
        index = LocalKVBlockIndex()
        stored = BlockStored([b"h1"], None, [1, 2], 2)
        index.apply_batch(0, Batch([stored]))
        index.apply_batch(1, Batch([stored]))
        index.apply_batch(2, Batch([BlockRemoved([b"h1"])]))
        self.assertEqual(index.snapshot()["block_count"], 1)

        index.apply_batch(3, Batch([BlockRemoved([b"h1"])]))
        self.assertEqual(index.snapshot()["block_count"], 0)

    def test_sequence_gap_invalidates_until_clear(self):
        index = LocalKVBlockIndex()
        stored = BlockStored([b"h1"], None, [1, 2], 2)
        index.apply_batch(0, Batch([stored]))
        index.apply_batch(2, Batch())

        snapshot = index.snapshot()
        self.assertFalse(snapshot["trusted"])
        self.assertEqual(snapshot["block_count"], 0)
        self.assertEqual(snapshot["sequence_gaps"], 1)

        index.apply_batch(3, Batch([AllBlocksCleared(), stored]))
        self.assertTrue(index.snapshot()["trusted"])
        self.assertEqual(index.snapshot()["block_count"], 1)

    def test_bounded_snapshot_keeps_root_prefix_first(self):
        index = LocalKVBlockIndex()
        tokens = list(range(1, 7))
        index.apply_batch(
            0,
            Batch([BlockStored([b"h1", b"h2", b"h3"], None, tokens, 2)]),
        )
        snapshot = index.snapshot(max_fingerprints=1)
        snapshot.update({"captured_at": 100.0, "consumer_alive": True})

        self.assertEqual(snapshot["published_block_count"], 1)
        self.assertEqual(
            snapshot["block_fingerprints"],
            token_block_fingerprints(tokens, 2)[:1],
        )
        self.assertEqual(
            match_prefix_blocks(tokens, {KV_ROUTING_STATS_KEY: snapshot}, now=100.0),
            1,
        )

    def test_stale_or_dead_consumer_stats_do_not_match(self):
        index = LocalKVBlockIndex()
        tokens = [1, 2]
        index.apply_batch(0, Batch([BlockStored([b"h1"], None, tokens, 2)]))
        stats = _routing_stats(index, now=90.0)
        self.assertEqual(match_prefix_blocks(tokens, stats, now=100.0, max_age_s=5.0), 0)

        stats = _routing_stats(index, now=100.0)
        stats[KV_ROUTING_STATS_KEY]["consumer_alive"] = False
        self.assertEqual(match_prefix_blocks(tokens, stats, now=100.0), 0)

    def test_gpu_and_cpu_copies_share_one_resident_record(self):
        index = LocalKVBlockIndex()
        stored_gpu = BlockStored([b"h1"], None, [1, 2], 2, medium="GPU")
        stored_cpu = BlockStored([b"h1"], None, [1, 2], 2, medium="CPU")
        index.apply_batch(
            0,
            Batch([stored_gpu, stored_cpu]),
        )
        index.apply_batch(1, Batch([BlockRemoved([b"h1"], medium="GPU")]))
        self.assertEqual(index.snapshot()["block_count"], 1)

        index.apply_batch(2, Batch([BlockRemoved([b"h1"], medium="CPU")]))
        self.assertEqual(index.snapshot()["block_count"], 0)


class TestKVEventsConfig(unittest.TestCase):
    def test_local_endpoint_overrides_user_transport_but_preserves_limits(self):
        config = build_local_kv_events_config(
            '{"buffer_steps": 42, "endpoint": "tcp://old:5557"}',
            endpoint="ipc:///tmp/replica.sock",
            topic="neutree-kv-events",
        )
        self.assertEqual(config["buffer_steps"], 42)
        self.assertEqual(config["endpoint"], "ipc:///tmp/replica.sock")
        self.assertEqual(config["topic"], "neutree-kv-events")
        self.assertTrue(config["enable_kv_cache_events"])
        self.assertEqual(config["publisher"], "zmq")
        self.assertIsNone(config["replay_endpoint"])

    def test_native_offload_enables_self_describing_events(self):
        config = build_native_cpu_offload_config(None)
        self.assertTrue(
            config["kv_connector_extra_config"]["self_describing_kv_events"]
        )


if __name__ == "__main__":
    unittest.main()
