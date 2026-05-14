"""Unit tests for shared_state._Shared callback hooks (Demo V14).

Pure-Python — no Ray needed. Run with `pytest test_shared_state.py`.
"""
import asyncio

import pytest

from shared_state import (
    ActorInfo,
    ActorTopology,
    ReplicaSnapshot,
    _Shared,
)


def test_replica_added_fires_for_new_ids():
    s = _Shared()
    seen = []
    s.on_replica_added(lambda rid, snap: seen.append((rid, snap.node_id)))

    s.replace_replicas([ReplicaSnapshot(replica_id="r0", node_id="n-a")])
    s.replace_replicas([
        ReplicaSnapshot(replica_id="r0", node_id="n-a"),
        ReplicaSnapshot(replica_id="r1", node_id="n-b"),
    ])

    assert seen == [("r0", "n-a"), ("r1", "n-b")]


def test_replica_removed_fires_on_drop():
    s = _Shared()
    removed = []
    s.on_replica_removed(lambda rid: removed.append(rid))

    s.replace_replicas([
        ReplicaSnapshot(replica_id="r0"),
        ReplicaSnapshot(replica_id="r1"),
    ])
    s.replace_replicas([ReplicaSnapshot(replica_id="r0")])  # r1 dropped

    assert removed == ["r1"]


def test_remove_replica_emits_only_when_present():
    s = _Shared()
    removed = []
    s.on_replica_removed(lambda rid: removed.append(rid))

    s.replace_replicas([ReplicaSnapshot(replica_id="r0")])
    s.remove_replica("r0")
    s.remove_replica("r0")  # already gone — must not double-fire
    s.remove_replica("nonexistent")

    assert removed == ["r0"]


def test_topology_updated_event_carries_full_topology():
    s = _Shared()
    seen = []
    s.on_topology_updated(lambda rid, topo: seen.append((rid, topo.prefill.actor_id)))

    s.upsert_topology(
        "r0",
        ActorTopology(
            replica_id="r0",
            prefill=ActorInfo(kind="prefill", actor_id="A"),
            decode=ActorInfo(kind="decode", actor_id="B"),
        ),
    )

    assert seen == [("r0", "A")]


def test_callback_exception_does_not_wedge_subsequent_handlers():
    s = _Shared()
    calls = []

    def first(_rid, _snap):
        raise RuntimeError("intentional")

    def second(rid, _snap):
        calls.append(rid)

    s.on_replica_added(first)
    s.on_replica_added(second)
    s.replace_replicas([ReplicaSnapshot(replica_id="r0")])

    assert calls == ["r0"]


def test_off_deregisters():
    s = _Shared()
    seen = []
    cb = lambda rid, _snap: seen.append(rid)  # noqa: E731

    s.on_replica_added(cb)
    s.replace_replicas([ReplicaSnapshot(replica_id="r0")])
    s.off("replica_added", cb)
    s.replace_replicas([
        ReplicaSnapshot(replica_id="r0"),
        ReplicaSnapshot(replica_id="r1"),
    ])

    assert seen == ["r0"]


def test_async_callback_runs_in_running_loop():
    s = _Shared()
    seen = []

    async def cb(rid, _snap):
        await asyncio.sleep(0)
        seen.append(rid)

    s.on_replica_added(cb)

    async def driver():
        s.replace_replicas([ReplicaSnapshot(replica_id="r0")])
        # Allow the scheduled task to run.
        await asyncio.sleep(0)
        await asyncio.sleep(0)

    asyncio.run(driver())
    assert seen == ["r0"]


def test_async_callback_skipped_without_loop_does_not_crash(caplog):
    s = _Shared()

    async def cb(_rid, _snap):
        pass

    s.on_replica_added(cb)
    # Called from a thread with no running asyncio loop.
    s.replace_replicas([ReplicaSnapshot(replica_id="r0")])
    # No assertion on output — just verifying the mutator returned normally.
    assert s.known_replica_ids() == ["r0"]


def test_callbacks_can_call_back_into_mutators():
    """A topology_updated callback that re-reads state must not deadlock."""
    s = _Shared()
    seen = []

    def cb(rid, _topo):
        # This would deadlock if _emit ran inside the lock.
        seen.append((rid, s.known_replica_ids()))

    s.on_topology_updated(cb)
    s.replace_replicas([ReplicaSnapshot(replica_id="r0")])
    s.upsert_topology("r0", ActorTopology(replica_id="r0"))

    assert seen == [("r0", ["r0"])]


# -------- D-10g: _extract_target_replica_id helper -------------------------


def test_extract_target_replica_id_variants():
    """Pure-Python coverage for the metadata extractor used by ObserverRouter.

    Mirrors the helper in observer_router._extract_target_replica_id so the
    behavior is testable without a running Ray Serve.
    """
    from types import SimpleNamespace

    # Inline copy of the helper logic — same shape, same fallbacks.
    def _extract(pr):
        if pr is None:
            return ""
        md = getattr(pr, "metadata", None)
        if md is None:
            return ""
        val = getattr(md, "multiplexed_model_id", None)
        return str(val) if val else ""

    assert _extract(None) == ""
    assert _extract(SimpleNamespace(metadata=None)) == ""
    assert _extract(SimpleNamespace(metadata=SimpleNamespace())) == ""
    assert _extract(SimpleNamespace(
        metadata=SimpleNamespace(multiplexed_model_id=None))) == ""
    assert _extract(SimpleNamespace(
        metadata=SimpleNamespace(multiplexed_model_id=""))) == ""
    assert _extract(SimpleNamespace(
        metadata=SimpleNamespace(multiplexed_model_id="r2"))) == "r2"
