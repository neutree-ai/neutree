"""Module-level _SHARED view for PDIngress.

Architecture invariant being validated (Demo V10):
    Inside a single Python process (the PDIngress replica), a module-level
    singleton acts as a coherent shared view between the Ray Serve RequestRouter
    callbacks (update_replicas / on_replica_actor_died) and the FastAPI request
    handlers. Multiple ingress processes (num_replicas > 1) each maintain their
    own _SHARED — MVP keeps this assumption.

Data model:
    serve_replicas: {replica_id_str -> ReplicaSnapshot}
        Populated by ObserverRouter.update_replicas. Each snapshot carries the
        replica_id, the node_id reported by Ray Serve, and the wall-clock time
        we last observed it.

    actor_topology: {replica_id_str -> ActorTopology}
        Populated lazily by PDIngress when it first needs to know which node
        the PrefillActor / DecodeActor of a given backend replica live on.
        MVP will refresh on a TTL (~100ms) per project memory; Demo keeps it
        observed-once.

Event hooks (Demo V14):
    _Shared accepts callback registration for three events:
        replica_added     — fired with (replica_id, ReplicaSnapshot)
        replica_removed   — fired with (replica_id,)
        topology_updated  — fired with (replica_id, ActorTopology)
    PDIngress uses replica_added to schedule an immediate get_actor_topology
    pull, replacing the request-path lazy refresh.

    Callbacks may be sync or async. Async callbacks are scheduled via the
    running asyncio loop; if no loop is found (mutation came from a non-loop
    thread), the callback is logged and skipped — MVP can revisit with a
    dedicated dispatcher loop if Ray Serve internals turn out to mutate from
    a background thread.
"""
from __future__ import annotations

import asyncio
import logging
import threading
import time
from dataclasses import dataclass, field, asdict
from typing import Any, Awaitable, Callable, Dict, List, Optional, Union


log = logging.getLogger("pd_ingress.shared")

# A callback can be a plain function or a coroutine function. Return value is
# ignored either way; exceptions are caught + logged so a buggy callback
# cannot wedge the state machine.
CallbackFn = Callable[..., Union[None, Awaitable[None]]]

EVENT_REPLICA_ADDED = "replica_added"
EVENT_REPLICA_REMOVED = "replica_removed"
EVENT_TOPOLOGY_UPDATED = "topology_updated"
_VALID_EVENTS = (EVENT_REPLICA_ADDED, EVENT_REPLICA_REMOVED, EVENT_TOPOLOGY_UPDATED)


@dataclass
class ReplicaSnapshot:
    replica_id: str
    node_id: str = ""
    observed_at: float = 0.0


@dataclass
class ActorInfo:
    """Per-actor identity / placement inside one PDCollocatedBackend replica.

    kind      — "prefill" or "decode"
    actor_id  — Ray ActorID (hex string), stable across calls
    node_id   — Ray node_id where this actor's process lives
    gpu_ids   — Ray-visible GPU indices (subset of CUDA_VISIBLE_DEVICES)
    healthy   — True when the actor reported its identity successfully
    """
    kind: str = ""
    actor_id: str = ""
    node_id: str = ""
    gpu_ids: List[int] = field(default_factory=list)
    healthy: bool = False


@dataclass
class ActorTopology:
    """Per-PDCollocatedBackend-replica actor placement.

    replica_id        — canonical Ray Serve ReplicaID (matches ObserverRouter
                        / _SHARED.serve_replicas key)
    replica_actor_id  — Ray ActorID of the Serve replica process itself
                        (different from the inner PrefillActor / DecodeActor)
    replica_node      — node_id of the Serve replica process
    global_rank       — Ray Serve 2.53 native rank: 0..world_size-1 across
                        all replicas of the deployment. ★ Used as the
                        replica index for plan.Ports lookup.
    node_rank         — Ray Serve native: per-node ordinal
    local_rank        — Ray Serve native: rank within node
    world_size        — total replica count at the time of observation
    pg_id             — placement_group id (hex string form)
    prefill           — ActorInfo for the inner PrefillActor
    decode            — ActorInfo for the inner DecodeActor
    same_host         — prefill.node_id == decode.node_id (and == replica_node
                        in Phase 0 since the Serve replica owns the PG)
    observed_at       — wall-clock time we fetched this from the backend replica
    """
    replica_id: str = ""
    replica_actor_id: str = ""
    replica_node: str = ""
    global_rank: int = -1
    node_rank: int = -1
    local_rank: int = -1
    world_size: int = 0
    pg_id: str = ""
    prefill: ActorInfo = field(default_factory=ActorInfo)
    decode: ActorInfo = field(default_factory=ActorInfo)
    same_host: bool = False
    observed_at: float = 0.0


@dataclass
class _Shared:
    serve_replicas: Dict[str, ReplicaSnapshot] = field(default_factory=dict)
    actor_topology: Dict[str, ActorTopology] = field(default_factory=dict)
    last_update_ts: float = 0.0
    _lock: threading.Lock = field(default_factory=threading.Lock, repr=False)
    _callbacks: Dict[str, List[CallbackFn]] = field(
        default_factory=lambda: {ev: [] for ev in _VALID_EVENTS}, repr=False
    )

    # ---------- callback registration (push-driven model) ----------

    def on(self, event: str, fn: CallbackFn) -> None:
        """Register a callback. Idempotent on (event, fn) pair."""
        if event not in _VALID_EVENTS:
            raise ValueError(f"unknown event {event!r}; expected one of {_VALID_EVENTS}")
        with self._lock:
            handlers = self._callbacks[event]
            if fn not in handlers:
                handlers.append(fn)

    def off(self, event: str, fn: CallbackFn) -> None:
        """Deregister a callback. No-op if not registered."""
        if event not in _VALID_EVENTS:
            return
        with self._lock:
            handlers = self._callbacks[event]
            if fn in handlers:
                handlers.remove(fn)

    def on_replica_added(self, fn: CallbackFn) -> None:
        self.on(EVENT_REPLICA_ADDED, fn)

    def on_replica_removed(self, fn: CallbackFn) -> None:
        self.on(EVENT_REPLICA_REMOVED, fn)

    def on_topology_updated(self, fn: CallbackFn) -> None:
        self.on(EVENT_TOPOLOGY_UPDATED, fn)

    def _emit(self, event: str, *args: Any) -> None:
        """Fire all callbacks for `event`. Called WITHOUT the lock held.
        Async callbacks are scheduled via the running loop; sync run inline.
        Exceptions in any callback are caught + logged.
        """
        with self._lock:
            handlers = list(self._callbacks.get(event, ()))
        for fn in handlers:
            try:
                if asyncio.iscoroutinefunction(fn):
                    try:
                        loop = asyncio.get_running_loop()
                    except RuntimeError:
                        log.warning(
                            "[_SHARED] async callback %s for event %s skipped: "
                            "no running asyncio loop in this thread",
                            getattr(fn, "__qualname__", repr(fn)), event,
                        )
                        continue
                    loop.create_task(fn(*args))
                else:
                    fn(*args)
            except Exception as exc:  # noqa: BLE001 — never let a callback wedge state
                log.warning(
                    "[_SHARED] callback %s for event %s raised: %s",
                    getattr(fn, "__qualname__", repr(fn)), event, exc,
                )

    # ---------- state mutators (each fires events on real changes) ----------

    def replace_replicas(self, snapshots: List[ReplicaSnapshot]) -> None:
        """Atomically swap the known-replica set. Old topology entries for
        replicas that disappeared are evicted; surviving entries keep their
        cached ActorTopology.

        Fires replica_added(rid, snap) for newly seen ids and
        replica_removed(rid) for ids that fell out of the set.
        """
        with self._lock:
            now = time.time()
            old_ids = set(self.serve_replicas.keys())
            new_serve: Dict[str, ReplicaSnapshot] = {}
            for snap in snapshots:
                snap.observed_at = now
                new_serve[snap.replica_id] = snap
            new_ids = set(new_serve.keys())
            added = [(rid, new_serve[rid]) for rid in (new_ids - old_ids)]
            removed = sorted(old_ids - new_ids)
            # evict topology cache entries for removed replicas
            for stale_id in removed:
                self.actor_topology.pop(stale_id, None)
            self.serve_replicas = new_serve
            self.last_update_ts = now

        # Emit outside the lock — callbacks may schedule tasks that call back
        # into mutators (e.g. upsert_topology after a get_actor_topology pull).
        for rid, snap in added:
            self._emit(EVENT_REPLICA_ADDED, rid, snap)
        for rid in removed:
            self._emit(EVENT_REPLICA_REMOVED, rid)

    def remove_replica(self, replica_id: str) -> None:
        """Called by ObserverRouter.on_replica_actor_died."""
        emitted = False
        with self._lock:
            if replica_id in self.serve_replicas:
                self.serve_replicas.pop(replica_id, None)
                self.actor_topology.pop(replica_id, None)
                self.last_update_ts = time.time()
                emitted = True
        if emitted:
            self._emit(EVENT_REPLICA_REMOVED, replica_id)

    def upsert_topology(self, replica_id: str, topo: ActorTopology) -> None:
        """Called by PDIngress after pulling topology from a backend replica."""
        with self._lock:
            topo.observed_at = time.time()
            self.actor_topology[replica_id] = topo
        self._emit(EVENT_TOPOLOGY_UPDATED, replica_id, topo)

    # ---------- read APIs ----------

    def snapshot(self) -> Dict[str, object]:
        """Return a JSON-serializable point-in-time view. Used by /v1/topology."""
        with self._lock:
            return {
                "last_update_ts": self.last_update_ts,
                "serve_replicas": {k: asdict(v) for k, v in self.serve_replicas.items()},
                "actor_topology": {k: asdict(v) for k, v in self.actor_topology.items()},
            }

    def known_replica_ids(self) -> List[str]:
        with self._lock:
            return list(self.serve_replicas.keys())


# Module-level singleton. All ObserverRouter instances + the PDIngress request
# handlers in the *same* Python process share this reference.
_SHARED = _Shared()


def get_shared() -> _Shared:
    """Accessor — keep all reads/writes through this for future swap-out."""
    return _SHARED
