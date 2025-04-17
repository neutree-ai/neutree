import hashlib
import logging
from typing import Dict, List, Optional

from ray.serve._private.common import ReplicaID, RunningReplicaInfo, ReplicaQueueLengthInfo
from ray.serve._private.constants import SERVE_LOGGER_NAME
from ray.serve._private.replica_scheduler.common import (
    PendingRequest,
    ReplicaQueueLengthCache,
)
from ray.serve._private.replica_scheduler.replica_scheduler import ReplicaScheduler
from ray.serve._private.replica_scheduler.replica_wrapper import RunningReplica

logger = logging.getLogger(SERVE_LOGGER_NAME)

class StaticHashReplicaScheduler(ReplicaScheduler):
    """A scheduler that routes requests to replicas based on payload hash.
    
    This scheduler ensures that identical payloads are always routed to the same replica.
    The scheduling is deterministic based on the hash of the request payload.
    """

    def __init__(
        self,
        get_curr_time_s=None,
        create_replica_wrapper_func=None,
    ):
        # Current replicas available to be scheduled
        self._replicas: Dict[ReplicaID, RunningReplica] = {}
        self._replica_queue_len_cache = ReplicaQueueLengthCache(
            get_curr_time_s=get_curr_time_s,
        )
        self._create_replica_wrapper_func = create_replica_wrapper_func
        # List form of replicas for easier indexing
        self._replica_list = []
        # Track whether we're using the queue length cache
        self._use_replica_queue_len_cache = True
        
        logger.info("Initialized StaticHashReplicaScheduler")

    async def choose_replica_for_request(
        self, pending_request: PendingRequest, *, is_retry: bool = False
    ) -> RunningReplica:
        """Choose a replica based on the hash of the request payload."""
        if not self._replicas:
            logger.warning("No replicas available for static hash scheduling")
            return None

        # Extract the payload from the PendingRequest
        payload = pending_request.args
        request_id = pending_request.metadata.request_id
        
        # If we have no payload, fall back to using request_id
        if not payload:
            payload_str = str(request_id)
            logger.info(f"No payload found, using request_id: {request_id}")
        else:
            payload_str = str(payload)
        
        # Calculate a hash of the payload
        payload_hash = hashlib.md5(payload_str.encode()).hexdigest()
        
        # Use the hash to select a replica
        idx = int(payload_hash, 16) % len(self._replica_list)
        selected_replica = self._replica_list[idx]
        
        logger.info(
            f"StaticHashScheduler: Payload hash={payload_hash[:8]}..., "
            f"Selected Replica={selected_replica.replica_id} "
            f"(index {idx} of {len(self._replica_list)})"
        )
        
        return selected_replica

    def create_replica_wrapper(
        self, replica_info: RunningReplicaInfo
    ) -> RunningReplica:
        """Create a new replica wrapper."""
        if self._create_replica_wrapper_func:
            return self._create_replica_wrapper_func(replica_info)
        return RunningReplica(replica_info)

    def update_replicas(self, replicas: List[RunningReplica]):
        """Update the list of available replicas."""
        self._replicas = {replica.replica_id: replica for replica in replicas}
        self._replica_list = list(self._replicas.values())
        logger.info(f"StaticHashScheduler: Updated replicas. Total: {len(self._replicas)}")
        
        # Log the replica IDs for debugging
        replica_ids = [r.replica_id for r in self._replica_list]
        logger.debug(f"StaticHashScheduler: Current replicas: {replica_ids}")

    def on_replica_actor_died(self, replica_id: ReplicaID):
        """Handle a replica that has died."""
        if replica_id in self._replicas:
            del self._replicas[replica_id]
            self._replica_list = list(self._replicas.values())
            logger.warning(
                f"StaticHashScheduler: Replica {replica_id} died. "
                f"Remaining: {len(self._replicas)}"
            )

    def on_replica_actor_unavailable(self, replica_id: ReplicaID):
        """Handle a replica that is unavailable."""
        # We don't remove the replica here, just invalidate the cache
        self._replica_queue_len_cache.invalidate_key(replica_id)
        logger.warning(f"StaticHashScheduler: Replica {replica_id} unavailable.")

    def on_new_queue_len_info(
        self, replica_id: ReplicaID, queue_len_info: ReplicaQueueLengthInfo
    ):
        """Update queue length cache with new info received from replica."""
        if self._use_replica_queue_len_cache:
            self._replica_queue_len_cache.update(
                replica_id, queue_len_info.num_ongoing_requests
            )
            logger.debug(
                f"StaticHashScheduler: Updated queue length for {replica_id}: "
                f"{queue_len_info.num_ongoing_requests}"
            )

    @property
    def replica_queue_len_cache(self) -> ReplicaQueueLengthCache:
        """Return the queue length cache."""
        return self._replica_queue_len_cache

    @property
    def curr_replicas(self) -> Dict[ReplicaID, RunningReplica]:
        """Return the current replicas."""
        return self._replicas
