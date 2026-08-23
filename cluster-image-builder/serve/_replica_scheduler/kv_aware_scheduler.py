"""Ray Serve request router backed by replica-local vLLM KV events."""

import logging
import time
from typing import Any, List, Optional

from ray.serve._private.constants import SERVE_LOGGER_NAME
from ray.serve._private.request_router.common import PendingRequest
from ray.serve._private.request_router.replica_wrapper import RunningReplica
from ray.serve._private.request_router.request_router import RequestRouter

from serve._kv_cache import KV_ROUTING_METADATA_KEY, match_prefix_blocks


logger = logging.getLogger(SERVE_LOGGER_NAME)


class KVAwareReplicaScheduler(RequestRouter):
    """Prefer replicas that report the longest resident prompt prefix.

    Missing, stale, or untrusted index data returns all candidates at the same
    rank so Ray's normal queue-length selection remains the fallback.
    """

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self._max_index_age_s = 5.0
        self._min_matched_blocks = 1

    def initialize_state(
        self,
        max_index_age_s: float = 5.0,
        min_matched_blocks: int = 1,
    ):
        self._max_index_age_s = max(0.1, float(max_index_age_s))
        self._min_matched_blocks = max(1, int(min_matched_blocks))
        logger.info(
            "Initialized KVAwareReplicaScheduler with max_index_age_s=%s, "
            "min_matched_blocks=%s",
            self._max_index_age_s,
            self._min_matched_blocks,
        )

    @staticmethod
    def _extract_token_ids(payload: Any) -> Optional[list[int]]:
        if isinstance(payload, (tuple, list)) and payload:
            request_data = payload[0]
        elif isinstance(payload, dict):
            request_data = payload
        else:
            return None
        if not isinstance(request_data, dict):
            return None

        metadata = request_data.get(KV_ROUTING_METADATA_KEY)
        if not isinstance(metadata, dict) or metadata.get("version") != 1:
            return None
        token_ids = metadata.get("token_ids")
        if not isinstance(token_ids, list) or not token_ids:
            return None
        if any(
            not isinstance(token_id, int)
            or isinstance(token_id, bool)
            or token_id < 0
            for token_id in token_ids
        ):
            return None
        return token_ids

    async def choose_replicas(
        self,
        candidate_replicas: List[RunningReplica],
        pending_request: Optional[PendingRequest] = None,
    ) -> List[List[RunningReplica]]:
        if not candidate_replicas:
            return [[]]
        if pending_request is None:
            return [candidate_replicas]

        token_ids = self._extract_token_ids(pending_request.args)
        if token_ids is None:
            return [candidate_replicas]

        now = time.time()
        scores = {
            replica.replica_id: match_prefix_blocks(
                token_ids,
                replica.routing_stats,
                now=now,
                max_age_s=self._max_index_age_s,
            )
            for replica in candidate_replicas
        }
        best_score = max(scores.values(), default=0)
        if best_score < self._min_matched_blocks:
            logger.debug("KV-aware routing fallback: no resident prefix match")
            return [candidate_replicas]

        preferred = [
            replica
            for replica in candidate_replicas
            if scores[replica.replica_id] == best_score
        ]
        fallback = [
            replica
            for replica in candidate_replicas
            if scores[replica.replica_id] != best_score
        ]
        logger.info(
            "KV-aware routing matched %d blocks on replica(s)=%s",
            best_score,
            [str(replica.replica_id) for replica in preferred],
        )
        return [preferred, fallback] if fallback else [preferred]
