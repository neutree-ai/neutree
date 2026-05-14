"""ObserverRouter — a Ray Serve RequestRouter that maintains a global view of
PDCollocatedBackend replicas inside the PDIngress process.

Demo (Phase 0) responsibilities:
    - Override `update_replicas(replicas)` to mirror the replica set into
      module-level `_SHARED.serve_replicas` (validates V10).
    - Override `on_replica_actor_died(replica_id)` to evict from `_SHARED`
      (validates V11).
    - Override `choose_replicas(...)` to do a deterministic round-robin pick
      so the dispatch path provably reads `_SHARED`. Falls back to the
      candidate list if `_SHARED` is empty (cold start race).

MVP (PR-ingress-lib) replaces choose_replicas with the full Ingress-as-Decider
pipeline: decode-first CHWBL(session-id) → same-host prefill CHWBL(prompt prefix).
ObserverRouter remains the topology observer; the scheduling primitives live in
serve/_scheduler/.
"""
from __future__ import annotations

import itertools
import logging
from typing import List, Optional

from ray.serve._private.common import ReplicaID
from ray.serve._private.constants import SERVE_LOGGER_NAME
from ray.serve._private.request_router.common import PendingRequest
from ray.serve._private.request_router.replica_wrapper import RunningReplica
from ray.serve._private.request_router.request_router import RequestRouter

from .shared_state import ReplicaSnapshot, get_shared


logger = logging.getLogger(SERVE_LOGGER_NAME)


def _extract_node_id(replica: RunningReplica) -> str:
    """Best-effort extraction of the Ray node_id from a RunningReplica.

    Ray's internal API has shifted across versions (node_id attribute vs
    replica_info.node_id). We probe a few known fields and fall back to "".
    """
    for attr in ("node_id", "_node_id"):
        v = getattr(replica, attr, None)
        if isinstance(v, str) and v:
            return v
    info = getattr(replica, "replica_info", None)
    if info is not None:
        v = getattr(info, "node_id", None)
        if isinstance(v, str) and v:
            return v
    return ""


class ObserverRouter(RequestRouter):
    """RequestRouter that observes replica topology and round-robin dispatches."""

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self._cursor = itertools.count()
        self._shared = get_shared()

    def initialize_state(self, **kwargs):
        """Custom kwargs from request_router_kwargs; Demo accepts none."""
        logger.info("[ObserverRouter] initialized (Demo: topology observer + round-robin)")

    # ----- topology observation (V10/V11) -----

    def update_replicas(self, replicas: List[RunningReplica]):
        snapshots = [
            ReplicaSnapshot(
                replica_id=str(r.replica_id),
                node_id=_extract_node_id(r),
            )
            for r in replicas
        ]
        self._shared.replace_replicas(snapshots)
        super().update_replicas(replicas)
        logger.info(
            "[ObserverRouter] update_replicas: total=%d -> _SHARED.serve_replicas=%s",
            len(replicas),
            [s.replica_id for s in snapshots],
        )

    def on_replica_actor_died(self, replica_id: ReplicaID):
        self._shared.remove_replica(str(replica_id))
        super().on_replica_actor_died(replica_id)
        logger.warning("[ObserverRouter] replica died: %s", replica_id)

    # ----- dispatch (proves _SHARED is consumed) -----

    async def choose_replicas(
        self,
        candidate_replicas: List[RunningReplica],
        pending_request: Optional[PendingRequest] = None,
    ) -> List[List[RunningReplica]]:
        if not candidate_replicas:
            logger.warning("[ObserverRouter] no candidates available")
            return [[]]

        known_ids = self._shared.known_replica_ids()
        if not known_ids:
            # Cold start: _SHARED hasn't been populated yet → fall back to the
            # raw candidate list and let Ray Serve's default selection win.
            return [candidate_replicas]

        # Deterministic round-robin over known_ids intersected with candidates.
        candidate_map = {str(r.replica_id): r for r in candidate_replicas}
        ordered_known = [rid for rid in known_ids if rid in candidate_map]
        if not ordered_known:
            return [candidate_replicas]

        idx = next(self._cursor) % len(ordered_known)
        picked_id = ordered_known[idx]
        picked = candidate_map[picked_id]
        logger.debug("[ObserverRouter] round-robin picked %s (idx=%d/%d)",
                     picked_id, idx, len(ordered_known))
        return [[picked]]
