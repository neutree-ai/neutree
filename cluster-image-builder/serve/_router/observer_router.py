"""ObserverRouter — a Ray Serve RequestRouter that maintains a global view of
PDCollocatedBackend replicas inside the PDRouter process.

Responsibilities:
    - Override `update_replicas(replicas)` to mirror the replica set into
      module-level `_SHARED.serve_replicas` (V10).
    - Override `on_replica_actor_died(replica_id)` to evict from `_SHARED`
      (V11).
    - Override `choose_replicas(...)` to honor an explicit caller-supplied
      `multiplexed_model_id="<ReplicaID>"` as a direct ReplicaID selector.
      Target misses return no candidates because PDRouter has already selected
      local prefill/decode ranks for the target RoleGroup.

Namespace policy for multiplexed_model_id:
    PDRouter uses this metadata channel exclusively for direct ReplicaID
    dispatch. Any non-empty value is treated as a ReplicaID target and must
    match one of Ray Serve's current candidates.

    Caller side example:
        handle.options(multiplexed_model_id="default#ep#PDCB#abc"
                       ).method.remote()         # -> exact replica dispatch

We inherit `MultiplexMixin` so its state-tracking machinery
(`_multiplexed_model_id_to_replica_ids` etc.) stays fresh via the super()
chain in `update_replicas`. This router does not query that state, but a future
subclass can — meaning we don't have to re-architect when LoRA /
SGLang routing semantics are added.

PDRouter owns the full Router-as-Decider pipeline: decode-first CHWBL on
prompt prefix, then same-host local-group prefill CHWBL. ObserverRouter remains
the topology observer + dispatch primitive.
"""
from __future__ import annotations

import logging
from typing import List, Optional

from ray.serve._private.common import ReplicaID
from ray.serve._private.constants import SERVE_LOGGER_NAME
from ray.serve._private.request_router.common import PendingRequest
from ray.serve._private.request_router.replica_wrapper import RunningReplica
from ray.serve._private.request_router.request_router import RequestRouter

# MultiplexMixin's home path has shifted across Ray versions. Probe both known
# locations so the container build doesn't pin Ray too tightly. If
# neither import works, fail loudly so the operator updates the import rather
# than silently losing forward-compatibility.
try:
    from ray.serve._private.request_router.request_router import MultiplexMixin
except ImportError:  # pragma: no cover — exercised only on Ray API drift
    try:
        from ray.serve._private.request_router.multiplex_mixin import MultiplexMixin
    except ImportError as _e:
        raise ImportError(
            "Ray Serve MultiplexMixin not found at either of the known "
            "locations (ray.serve._private.request_router.request_router or "
            ".multiplex_mixin). Ray version likely changed; update this "
            "import."
        ) from _e

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


def _replica_unique_id(replica_id_or_str) -> str:
    """Reduce any Ray ReplicaID representation to its short `unique_id`.

    Ray's stringification has shifted between releases — observed forms:
        Replica(id='<uid>', deployment='...', app='...')
        SERVE_REPLICA::<app>#<deployment>#<uid>
        <uid>                                  # plain
    We probe in order:
      1. `.unique_id` attribute on the object (cheapest, most accurate)
      2. trailing `#<uid>` segment (SERVE_REPLICA actor-name form)
      3. `id='<uid>'` substring (Replica(...) repr form)
      4. whole string (assume caller passed bare unique_id)
    """
    uid = getattr(replica_id_or_str, "unique_id", None)
    if isinstance(uid, str) and uid:
        return uid
    s = str(replica_id_or_str)
    if "#" in s:
        return s.rsplit("#", 1)[-1]
    if "id='" in s:
        try:
            return s.split("id='", 1)[1].split("'", 1)[0]
        except IndexError:
            pass
    return s


def _replica_id_matches(replica: RunningReplica, rid: str) -> bool:
    """True if `rid` selects this replica.

    Match by reducing both sides to the short `unique_id` token, so any
    user-facing repr is accepted (bare `dh8c06yy`, the actor-name form
    `SERVE_REPLICA::app#dep#dh8c06yy`, or the `Replica(id='dh8c06yy', ...)`
    repr). Exact-equality fast path first so a verbatim full-form target
    still wins without parsing.
    """
    if str(replica.replica_id) == rid:
        return True
    return _replica_unique_id(replica.replica_id) == _replica_unique_id(rid)


def _extract_target_replica_id(pending_request: Optional[PendingRequest]) -> str:
    """Read the caller-supplied direct-dispatch hint from request metadata.

    Returns the empty string when no hint is set or metadata is unavailable
    (we treat that as "router decides"). Robust to Ray version drift on
    the metadata attribute name.
    """
    if pending_request is None:
        return ""
    md = getattr(pending_request, "metadata", None)
    if md is None:
        return ""
    val = getattr(md, "multiplexed_model_id", None)
    return str(val) if val else ""


class ObserverRouter(MultiplexMixin, RequestRouter):
    """Passive RequestRouter: mirrors topology to _SHARED and obeys direct targets.

    Two responsibilities only:
      1. Topology observation — `update_replicas` / `on_replica_actor_died`
         mirror Ray Serve's view of PDCollocatedBackend replicas into
         module-level `_SHARED` so PDRouter can read it via callbacks.
      2. Direct dispatch — when PDRouter sets
         `multiplexed_model_id="<ReplicaID>"`, dispatch directly to that
         replica. Target miss returns no candidates; no target still defers
         to the framework default.

    Decision logic — CHWBL, prefix-cache aware routing, load balancing — does
    NOT live here. PDRouter owns those, calling `.options(multiplexed_
    model_id="<ReplicaID>")` on the backend handle to express the choice
    through the direct-dispatch channel this router obeys.
    """

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self._shared = get_shared()

    def initialize_state(self, **kwargs):
        """Custom kwargs from request_router_kwargs; accepts none."""
        logger.info(
            "[ObserverRouter] initialized (passive observer + ReplicaID "
            "direct dispatch; MultiplexMixin inherited for forward compat)"
        )

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
        # super() chain (MRO: ObserverRouter -> MultiplexMixin -> RequestRouter):
        #   - MultiplexMixin.update_replicas inspects each replica's advertised
        #     multiplexed_model_ids and updates its internal map. PDRouter
        #     performs its own route decision, but keeping this fresh preserves
        #     framework invariants for subclasses.
        #   - RequestRouter.update_replicas keeps self._replicas in sync.
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

    # ----- dispatch -----

    async def choose_replicas(
        self,
        candidate_replicas: List[RunningReplica],
        pending_request: Optional[PendingRequest] = None,
    ) -> List[List[RunningReplica]]:
        if not candidate_replicas:
            logger.warning("[ObserverRouter] no candidates available")
            return [[]]

        raw_target = _extract_target_replica_id(pending_request)

        # Any non-empty multiplexed_model_id is a bare ReplicaID direct target.
        if raw_target:
            rid = raw_target
            for r in candidate_replicas:
                if _replica_id_matches(r, rid):
                    logger.info(
                        "[ObserverRouter][pick/direct] target=%r matched=%r "
                        "(uid=%r) candidates=%d",
                        rid,
                        str(r.replica_id),
                        _replica_unique_id(r.replica_id),
                        len(candidate_replicas),
                    )
                    return [[r]]
            # Target miss: PDRouter's chosen replica is no longer a candidate
            # (race: it died between PDRouter reading _SHARED and Ray Serve
            # updating this router's candidate set). Returning no candidates
            # is intentional: PDRouter also selected local prefill/decode
            # ranks for this domain, so framework re-pick would be wrong.
            candidate_dump = [
                {
                    "str": str(r.replica_id),
                    "uid": _replica_unique_id(r.replica_id),
                }
                for r in candidate_replicas
            ]
            logger.warning(
                "[ObserverRouter] direct target %r not in %d candidates; "
                "returning no candidate. target_uid=%r candidates=%s",
                raw_target,
                len(candidate_replicas),
                _replica_unique_id(rid),
                candidate_dump,
            )
            return [[]]

        # No target -> framework default
        # (pow2 etc). PDRouter is expected to set a target on every request,
        # so reaching here on the steady-state path indicates either a
        # non-PDRouter caller or a missing direct target.
        return [candidate_replicas]
