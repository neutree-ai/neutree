"""ObserverRouter — a Ray Serve RequestRouter that maintains a global view of
PDCollocatedBackend replicas inside the PDIngress process.

Demo (Phase 0) responsibilities:
    - Override `update_replicas(replicas)` to mirror the replica set into
      module-level `_SHARED.serve_replicas` (V10).
    - Override `on_replica_actor_died(replica_id)` to evict from `_SHARED`
      (V11).
    - Override `choose_replicas(...)` to honor an explicit caller-supplied
      `multiplexed_model_id="replica:<rid>"` as a direct ReplicaID selector
      (V15); falls back to deterministic round-robin over
      `_SHARED.known_replica_ids` when no hint is present.

Namespace policy for multiplexed_model_id:
    `multiplexed_model_id` is a shared channel — other features (LoRA
    adapters via @serve.multiplexed, SGLang custom routing like
    bootstrap_room, etc.) may want to use it too. We claim only the
    `"replica:"` prefix; values without that prefix are deferred to standard
    routing so MVP / future ingress code can subclass ObserverRouter to add
    LoRA or SGLang handling without re-architecting.

    Caller side examples:
        handle.options(multiplexed_model_id="replica:default#ep#PDCB#abc"
                       ).method.remote()         # → exact replica dispatch
        handle.options(multiplexed_model_id="lora-A").method.remote()
                                                  # → MultiplexMixin state /
                                                  #   round-robin fallback
        handle.options(multiplexed_model_id="bootstrap-room-42").method.remote()
                                                  # → currently round-robin;
                                                  #   MVP subclass adds handling

We inherit `MultiplexMixin` so its state-tracking machinery
(`_multiplexed_model_id_to_replica_ids` etc.) stays fresh via the super()
chain in `update_replicas`. Demo never queries that state, but a future
MVP subclass can — meaning we don't have to re-architect when LoRA /
SGLang routing semantics are added.

MVP (PR-ingress-lib) replaces `choose_replicas` with the full Ingress-as-
Decider pipeline: decode-first CHWBL(session-id) → same-host prefill
CHWBL(prompt prefix). ObserverRouter remains the topology observer + dispatch
primitive; the scheduling primitives live in serve/_scheduler/.
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

# MultiplexMixin's home path has shifted across Ray versions. Probe both known
# locations so the Demo container build doesn't pin Ray too tightly. If
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


# Namespace prefix used by PDIngress to direct-dispatch by ReplicaID.
# Caller side: `handle.options(multiplexed_model_id=f"{REPLICA_DISPATCH_PREFIX}{rid}")`.
# This prefix is intentionally explicit so future namespaces (LoRA, SGLang
# bootstrap_room, etc.) can coexist on the same `multiplexed_model_id` channel.
REPLICA_DISPATCH_PREFIX = "replica:"


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
    repr). Exact-equality fast path first so a verbatim full-form pin
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
    """RequestRouter that observes replica topology and dispatches.

    Dispatch rules (D-10g + D-10h):
        1. If caller set `multiplexed_model_id` starting with `"replica:"`,
           match the suffix exactly against `str(r.replica_id)`. Hit →
           direct dispatch. Miss → log warning, fall through to round-robin.
        2. If caller set `multiplexed_model_id` *without* `"replica:"`,
           Demo logs and falls through to round-robin. MVP subclasses
           override this branch to query MultiplexMixin's
           `_multiplexed_model_id_to_replica_ids` or implement
           engine-specific semantics (SGLang bootstrap_room etc.).
        3. Otherwise (no hint), deterministic round-robin over the
           `_SHARED.known_replica_ids()` view intersected with `candidates`.
    """

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self._cursor = itertools.count()
        self._shared = get_shared()

    def initialize_state(self, **kwargs):
        """Custom kwargs from request_router_kwargs; Demo accepts none."""
        logger.info(
            "[ObserverRouter] initialized (topology observer + 'replica:' "
            "namespace direct dispatch + round-robin fallback; "
            "MultiplexMixin inherited for forward compatibility)"
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
        #     multiplexed_model_ids and updates its internal map. Demo doesn't
        #     query this map, but keeping it fresh means MVP subclasses can.
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

        # ── (1) Explicit "replica:<rid>" namespace → direct dispatch ──
        raw_target = _extract_target_replica_id(pending_request)
        if raw_target.startswith(REPLICA_DISPATCH_PREFIX):
            rid = raw_target[len(REPLICA_DISPATCH_PREFIX):]
            for r in candidate_replicas:
                if _replica_id_matches(r, rid):
                    logger.info(
                        "[ObserverRouter][pick/direct] target=%s "
                        "candidates=%d", rid, len(candidate_replicas),
                    )
                    return [[r]]
            logger.warning(
                "[ObserverRouter] direct target %s not in current %d candidates; "
                "falling back to round-robin",
                raw_target,
                len(candidate_replicas),
            )
            # fall through
        elif raw_target:
            # ── (2) Other namespaces — defer to subclass / round-robin ──
            #
            # Demo doesn't claim non-"replica:" multiplexed_model_id values.
            # MVP routers (LoRA adapter routing, SGLang custom semantics) can
            # subclass ObserverRouter and override this branch to consult
            # MultiplexMixin state, e.g.
            #   replicas = getattr(self, "_multiplexed_model_id_to_replica_ids", {})
            #                   .get(raw_target)
            logger.debug(
                "[ObserverRouter] non-replica multiplex target %r; Demo defers "
                "to round-robin (MVP subclass should override)",
                raw_target,
            )
            # fall through

        # ── (3) Deterministic round-robin over _SHARED-known replicas ──
        known_ids = self._shared.known_replica_ids()
        if not known_ids:
            # Cold start: _SHARED not populated yet → let the framework pick
            # from the raw candidate list.
            logger.info(
                "[ObserverRouter][pick/cold] _SHARED empty; framework pick "
                "from candidates=%d", len(candidate_replicas),
            )
            return [candidate_replicas]

        candidate_map = {str(r.replica_id): r for r in candidate_replicas}
        ordered_known = [rid for rid in known_ids if rid in candidate_map]
        if not ordered_known:
            logger.info(
                "[ObserverRouter][pick/no-overlap] known=%d candidates=%d "
                "intersection=0; framework pick from candidates",
                len(known_ids), len(candidate_replicas),
            )
            return [candidate_replicas]

        idx = next(self._cursor) % len(ordered_known)
        picked_id = ordered_known[idx]
        picked = candidate_map[picked_id]
        logger.info(
            "[ObserverRouter][pick/rr] picked=%s idx=%d/%d known=%d candidates=%d",
            picked_id, idx, len(ordered_known),
            len(known_ids), len(candidate_replicas),
        )
        return [[picked]]
