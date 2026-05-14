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
"""
from __future__ import annotations

import threading
import time
from dataclasses import dataclass, field, asdict
from typing import Dict, List, Optional


@dataclass
class ReplicaSnapshot:
    replica_id: str
    node_id: str = ""
    observed_at: float = 0.0


@dataclass
class ActorTopology:
    """Per-PDCollocatedBackend-replica actor placement.

    pg_id          — placement_group id (truncated string form)
    prefill_node   — Ray node_id of the PrefillActor (1 bundle of the PG)
    decode_node    — Ray node_id of the DecodeActor (the other bundle)
    same_host      — convenience flag, True iff prefill_node == decode_node
    observed_at    — wall-clock time we fetched this from the backend replica
    """
    pg_id: str = ""
    prefill_node: str = ""
    decode_node: str = ""
    same_host: bool = False
    observed_at: float = 0.0


@dataclass
class _Shared:
    serve_replicas: Dict[str, ReplicaSnapshot] = field(default_factory=dict)
    actor_topology: Dict[str, ActorTopology] = field(default_factory=dict)
    last_update_ts: float = 0.0
    _lock: threading.Lock = field(default_factory=threading.Lock, repr=False)

    def replace_replicas(self, snapshots: List[ReplicaSnapshot]) -> None:
        """Atomically swap the known-replica set. Old topology entries for
        replicas that disappeared are evicted; surviving entries keep their
        cached ActorTopology.
        """
        with self._lock:
            now = time.time()
            new_serve: Dict[str, ReplicaSnapshot] = {}
            for snap in snapshots:
                snap.observed_at = now
                new_serve[snap.replica_id] = snap
            # evict topology cache entries for removed replicas
            for stale_id in list(self.actor_topology.keys()):
                if stale_id not in new_serve:
                    del self.actor_topology[stale_id]
            self.serve_replicas = new_serve
            self.last_update_ts = now

    def remove_replica(self, replica_id: str) -> None:
        """Called by ObserverRouter.on_replica_actor_died."""
        with self._lock:
            self.serve_replicas.pop(replica_id, None)
            self.actor_topology.pop(replica_id, None)
            self.last_update_ts = time.time()

    def upsert_topology(self, replica_id: str, topo: ActorTopology) -> None:
        """Called by PDIngress after pulling topology from a backend replica."""
        with self._lock:
            topo.observed_at = time.time()
            self.actor_topology[replica_id] = topo

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
