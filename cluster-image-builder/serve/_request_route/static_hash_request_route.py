from ray.serve.request_router import (
    FIFOMixin,
    LocalityMixin,
    MultiplexMixin,
    PendingRequest,
    RequestRouter,
    ReplicaID,
    ReplicaResult,
    RunningReplica,
)

from ray.serve._private.common import (
    DeploymentHandleSource,
    DeploymentID,
    ReplicaID,
    ReplicaQueueLengthInfo,
    RequestMetadata,
    RunningReplicaInfo,
)

from ray.actor import ActorHandle

from typing import (
    Dict,
    List,
    Optional,
    Callable,
)

import logging
import hashlib

from ray.serve._private.constants import SERVE_LOGGER_NAME
logger = logging.getLogger(SERVE_LOGGER_NAME)

class StaticHashRequestRouter(
    FIFOMixin, MultiplexMixin, LocalityMixin, RequestRouter
):

    def __init__(
        self,
        deployment_id: DeploymentID,
        handle_source: DeploymentHandleSource,
        self_actor_id: Optional[str] = None,
        self_actor_handle: Optional[ActorHandle] = None,
        use_replica_queue_len_cache: bool = False,
        get_curr_time_s: Optional[Callable[[], float]] = None,
        create_replica_wrapper_func: Optional[
            Callable[[RunningReplicaInfo], RunningReplica]
        ] = None,
        *args,
        **kwargs,
    ):
        super().__init__(
            deployment_id,
            handle_source,
            self_actor_id=self_actor_id,
            self_actor_handle=self_actor_handle,
            use_replica_queue_len_cache=use_replica_queue_len_cache,
            get_curr_time_s=get_curr_time_s,
            create_replica_wrapper_func=create_replica_wrapper_func,
            *args,
            **kwargs,
        )
        # List form of replicas for easier indexing
        self._replica_list = []

        logger.info("Initialized StaticHashRequestRouter")

    async def choose_replicas(
        self,
        candidate_replicas: List[RunningReplica],
        pending_request: Optional[PendingRequest] = None,
    ) -> List[List[RunningReplica]]:
        """Choose replicas based on the hash of the request payload."""
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
