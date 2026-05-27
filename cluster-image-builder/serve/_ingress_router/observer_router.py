"""ObserverRouter — a Ray Serve RequestRouter that maintains a global view of
PDCollocatedBackend replicas inside the PDIngress process.

Responsibilities:
    - Override `update_replicas(replicas)` to mirror the replica set into
      module-level `_SHARED.serve_replicas` (V10).
    - Override `on_replica_actor_died(replica_id)` to evict from `_SHARED`
      (V11).
    - Override `choose_replicas(...)` to honor an explicit caller-supplied
      `multiplexed_model_id="replica:<rid>"` as a direct ReplicaID selector.
      Pin misses return no candidates because PDIngress has already selected
      local prefill/decode ranks for the pinned RoleGroup.

Namespace policy for multiplexed_model_id:
    `multiplexed_model_id` is a shared channel — other features (LoRA
    adapters via @serve.multiplexed, SGLang custom routing like
    bootstrap_room, etc.) may want to use it too. We claim only the
    `"replica:"` prefix; values without that prefix are deferred to standard
    routing so future ingress code can subclass ObserverRouter to add
    LoRA or SGLang handling without re-architecting.

    Caller side examples:
        handle.options(multiplexed_model_id="replica:default#ep#PDCB#abc"
                       ).method.remote()         # → exact replica dispatch
        handle.options(multiplexed_model_id="lora-A").method.remote()
                                                  # → MultiplexMixin state /
                                                  #   framework fallback
        handle.options(multiplexed_model_id="bootstrap-room-42").method.remote()
                                                  # → currently framework default;
                                                  #   a future subclass may handle it

We inherit `MultiplexMixin` so its state-tracking machinery
(`_multiplexed_model_id_to_replica_ids` etc.) stays fresh via the super()
chain in `update_replicas`. This router does not query that state, but a future
subclass can — meaning we don't have to re-architect when LoRA /
SGLang routing semantics are added.

PDIngress owns the full Ingress-as-Decider pipeline: decode-first CHWBL on
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
    """Passive RequestRouter: mirrors topology to _SHARED and obeys pins.

    Two responsibilities only:
      1. Topology observation — `update_replicas` / `on_replica_actor_died`
         mirror Ray Serve's view of PDCollocatedBackend replicas into
         module-level `_SHARED` so PDIngress can read it via callbacks.
      2. Pin obedience — when the caller sets
         `multiplexed_model_id="replica:<rid>"`, dispatch directly to that
         replica. Pin miss returns no candidates; no pin / non-replica
         namespace still defer to the framework default.

    Decision logic — CHWBL, prefix-cache aware routing, load balancing — does
    NOT live here. PDIngress owns those, calling `.options(multiplexed_
    model_id="replica:<rid>")` on the backend handle to express the choice
    through the pin channel this router obeys.
    """

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self._shared = get_shared()

    def initialize_state(self, **kwargs):
        """Custom kwargs from request_router_kwargs; accepts none."""
        logger.info(
            "[ObserverRouter] initialized (passive observer + 'replica:' "
            "pin obedience; MultiplexMixin inherited for forward compat)"
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
        #     multiplexed_model_ids and updates its internal map. PDIngress
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

        # Explicit "replica:<rid>" pin -> exact dispatch.
        if raw_target.startswith(REPLICA_DISPATCH_PREFIX):
            rid = raw_target[len(REPLICA_DISPATCH_PREFIX):]
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
            # Pin miss: caller's chosen replica is no longer a candidate
            # (race: it died between PDIngress reading _SHARED and Ray Serve
            # updating this router's candidate set). Returning no candidates
            # is intentional: PDIngress also selected local prefill/decode
            # ranks for this RoleGroup, so framework re-pick would be wrong.
            candidate_dump = [
                {
                    "str": str(r.replica_id),
                    "uid": _replica_unique_id(r.replica_id),
                }
                for r in candidate_replicas
            ]
            logger.warning(
                "[ObserverRouter] pin %r not in %d candidates; returning no "
                "candidate. caller_uid=%r candidates=%s",
                raw_target,
                len(candidate_replicas),
                _replica_unique_id(rid),
                candidate_dump,
            )
            return [[]]

        elif raw_target:
            # Non-replica namespaces (LoRA adapter, SGLang bootstrap_room, …)
            # are the responsibility of subclasses that override this
            # branch and consult MultiplexMixin state. This router defers.
            logger.debug(
                "[ObserverRouter] non-replica multiplex target %r; defer to "
                "framework (custom subclass may override)",
                raw_target,
            )
            # fall through

        # No pin / non-replica namespace -> framework default
        # (pow2 etc). PDIngress is expected to set a pin on every request,
        # so reaching here on the steady-state path indicates either a
        # non-PDIngress caller or a custom namespace.
        return [candidate_replicas]
