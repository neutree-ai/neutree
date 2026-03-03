import hashlib
import logging
from typing import Dict, List, Optional

from ray.serve._private.common import ReplicaID
from ray.serve._private.constants import SERVE_LOGGER_NAME
from ray.serve._private.request_router.common import (
    PendingRequest,
)
from ray.serve._private.request_router.request_router import RequestRouter
from ray.serve._private.request_router.replica_wrapper import RunningReplica

logger = logging.getLogger(SERVE_LOGGER_NAME)


class StaticHashReplicaScheduler(RequestRouter):
    """A scheduler that routes requests to replicas based on payload hash.

    This scheduler ensures that identical payloads are always routed to the same replica.
    The scheduling is deterministic based on the hash of the request payload.
    """

    def __init__(
        self,
        *args,
        **kwargs,
    ):
        # Pass all arguments to parent RequestRouter class
        super().__init__(*args, **kwargs)

        # List form of replicas for easier indexing
        self._replica_list: List[RunningReplica] = []

        logger.info("Initialized StaticHashReplicaScheduler")

    async def choose_replicas(
        self,
        candidate_replicas: List[RunningReplica],
        pending_request: Optional[PendingRequest] = None,
    ) -> List[List[RunningReplica]]:
        """Choose replicas based on the hash of the request payload.

        Args:
            candidate_replicas: List of available replicas to choose from
            pending_request: The pending request to route (optional)

        Returns:
            List of replica lists ordered by priority based on payload hash.
        """
        if not candidate_replicas:
            logger.warning("No candidate replicas available for static hash scheduling")
            return [[]]

        # If no pending request, return all candidates as equal priority
        if pending_request is None:
            return [candidate_replicas]

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
        idx = int(payload_hash, 16) % len(candidate_replicas)
        selected_replica = candidate_replicas[idx]

        logger.info(
            f"StaticHashScheduler: Payload hash={payload_hash[:8]}..., "
            f"Selected Replica={selected_replica.replica_id} "
            f"(index {idx} of {len(candidate_replicas)})"
        )

        return [[selected_replica]]

    def update_replicas(self, replicas: List[RunningReplica]):
        """Update the list of available replicas."""
        # Call parent's update_replicas
        super().update_replicas(replicas)

        # Maintain our own replica list for indexing
        self._replica_list = list(self._replicas.values())

        logger.info(f"StaticHashScheduler: Updated replicas. Total: {len(self._replicas)}")

        # Log the replica IDs for debugging
        replica_ids = [r.replica_id for r in self._replica_list]
        logger.debug(f"StaticHashScheduler: Current replicas: {replica_ids}")

    def on_replica_actor_died(self, replica_id: ReplicaID):
        """Handle a replica that has died."""
        # Call parent's handler
        super().on_replica_actor_died(replica_id)

        # Update our replica list
        self._replica_list = list(self._replicas.values())

        logger.warning(
            f"StaticHashScheduler: Replica {replica_id} died. "
            f"Remaining: {len(self._replicas)}"
        )

