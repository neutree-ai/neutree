from concurrent.futures import thread
import hashlib
import logging
import bisect
from math import log
import threading
from typing import Dict, List, Set, Tuple, Optional

from ray.serve._private.common import ReplicaID, RunningReplicaInfo, ReplicaQueueLengthInfo
from ray.serve._private.constants import SERVE_LOGGER_NAME
from ray.serve._private.replica_scheduler.common import (
    PendingRequest,
    ReplicaQueueLengthCache,
)
from ray.serve._private.replica_scheduler.replica_scheduler import ReplicaScheduler
from ray.serve._private.replica_scheduler.replica_wrapper import RunningReplica

logger = logging.getLogger(SERVE_LOGGER_NAME)


class ConsistentHashReplicaScheduler(ReplicaScheduler):
    """A scheduler that routes requests using consistent hashing with bounded loads.

    This scheduler ensures that similar payloads are routed to the same replica
    while maintaining load balance and minimizing disruption when replicas are added or removed.

    The implementation uses Consistent Hashing with Bounded Loads (CHWBL) to balance
    between consistent routing and load distribution.
    """

    def __init__(
        self,
        get_curr_time_s=None,
        create_replica_wrapper_func=None,
        virtual_nodes_per_replica: int = 100,
        load_factor: float = 1.25,
        max_user_messages_for_cache: int = 2,
    ):
        # Current replicas available to be scheduled
        self._replicas: Dict[ReplicaID, RunningReplica] = {}
        self._replica_queue_len_cache = ReplicaQueueLengthCache(
            get_curr_time_s=get_curr_time_s,
            staleness_timeout_s=86400.0,  # 24 hours - prevent cache expiration for long-running requests
        )
        self._create_replica_wrapper_func = create_replica_wrapper_func

        # Consistent hashing settings
        self._virtual_nodes = virtual_nodes_per_replica
        self._load_factor = load_factor

        # Cache key extraction settings for chat completions
        self._max_user_messages_for_cache = max_user_messages_for_cache

        # Hash ring data structures
        self._hash_to_replica_id: Dict[int, str] = {}  # Maps hash points to replica IDs
        self._sorted_hashes: List[int] = []  # Sorted list of hash points for binary search

        # For tracking load
        self._use_replica_queue_len_cache = True

        # Lock for thread-safe updates
        self._load_lock = threading.Lock()

        logger.info(
            f"Initialized ConsistentHashReplicaScheduler with "
            f"{virtual_nodes_per_replica} virtual nodes per replica, "
            f"load factor of {load_factor}, "
            f"max_user_messages_for_cache={max_user_messages_for_cache}"
        )

    def _create_load_snapshot(self) -> Dict[ReplicaID, int]:
        """Create a snapshot of current replica loads (thread-safe).

        Returns:
            A dictionary mapping replica_id to its current load.
        """
        with self._load_lock:
            snapshot = {}
            for replica_id in self._replicas:
                load = self._replica_queue_len_cache.get(replica_id)
                snapshot[replica_id] = load if load is not None else 0

            return snapshot

    async def choose_replica_for_request(
        self, pending_request: PendingRequest, *, is_retry: bool = False
    ) -> RunningReplica:
        """Choose a replica based on consistent hashing with bounded load."""
        if not self._replicas or len(self._sorted_hashes) == 0:
            logger.warning("No replicas available for consistent hash scheduling")
            return None

        # Extract the payload from the PendingRequest
        payload = pending_request.args
        request_id = pending_request.metadata.request_id

        # Extract cache key for OpenAI-compatible chat completions
        cache_key = self._extract_cache_key(payload, request_id)

        # Calculate a hash of the cache key
        payload_hash = self._hash(cache_key)

        # Find initial replica using consistent hashing
        replica_hash, replica_idx = self._search(payload_hash)
        initial_replica_id = self._hash_to_replica_id[replica_hash]

        logger.debug(
            f"CHWBL: Initial lookup for payload hash {payload_hash} -> "
            f"replica {initial_replica_id}"
        )

        # Create load snapshot at the beginning to ensure consistency
        load_snapshot = self._create_load_snapshot()

        # Track if we've tried all replicas
        checked_replica_ids: Set[str] = set()
        default_replica_id = None

        # Start from the initial replica and check load constraints
        current_idx = replica_idx
        while len(checked_replica_ids) < len(self._replicas):
            current_hash = self._sorted_hashes[current_idx]
            current_replica_id = self._hash_to_replica_id[current_hash]

            # Skip duplicate replica IDs (from virtual nodes)
            if current_replica_id in checked_replica_ids:
                current_idx = (current_idx + 1) % len(self._sorted_hashes)
                continue

            checked_replica_ids.add(current_replica_id)

            # Get the replica
            replica = self._replicas.get(current_replica_id)
            if not replica:
                # This shouldn't happen with proper maintenance of hash ring
                logger.warning(f"Replica {current_replica_id} not found in _replicas")
                current_idx = (current_idx + 1) % len(self._sorted_hashes)
                continue

            # For first valid replica, save as default (will be used if no replica meets load factor)
            if default_replica_id is None:
                default_replica_id = current_replica_id

            # Check if this replica meets the load constraints using snapshot
            if self._check_load_with_snapshot(current_replica_id, load_snapshot):
                logger.info(
                    f"CHWBL: Selected replica {current_replica_id} after checking "
                    f"{len(checked_replica_ids)} replicas for payload hash {payload_hash}, "
                    f"snapshot_load={load_snapshot.get(current_replica_id, 0)}"
                )
                return replica

            # Move to next replica
            current_idx = (current_idx + 1) % len(self._sorted_hashes)

        # If no replica satisfies the load factor, use the default
        if default_replica_id:
            logger.warning(
                f"CHWBL: Using default replica {default_replica_id} "
                f"as no replica met load factor for payload hash {payload_hash}, "
                f"snapshot_load={load_snapshot.get(default_replica_id, 0)}"
            )
            return self._replicas[default_replica_id]

        # No replicas available at all
        return None

    def _check_load_with_snapshot(
        self, replica_id: str, load_snapshot: Dict[ReplicaID, int]
    ) -> bool:
        """Check if the replica meets the load constraints using a load snapshot.

        Args:
            replica_id: The replica to check
            load_snapshot: Pre-captured snapshot of all replica loads

        Returns:
            True if the replica can accept the request, False otherwise
        """
        load = load_snapshot.get(replica_id, 0)

        # Calculate average load across all replicas using snapshot
        total_load = sum(load_snapshot.values())
        avg_load = (total_load + 1) / len(self._replicas)  # +1 for the current request

        # Apply load factor threshold
        threshold = avg_load * self._load_factor

        logger.debug(
            f"CHWBL: Replica {replica_id} load={load}, "
            f"total_load={total_load}, avg_load={avg_load:.2f}, threshold={threshold:.2f}"
        )

        # Check if this replica is under the threshold (including the current request)
        return (load + 1) <= threshold

    def _get_replica_load(self, replica_id: str) -> Optional[int]:
        """Get the current load (queue length) for a replica."""
        return self._replica_queue_len_cache.get(replica_id)

    def _get_total_load(self) -> int:
        """Get the total load across all replicas."""
        total = 0
        for replica_id in self._replicas:
            load = self._replica_queue_len_cache.get(replica_id)
            if load is not None:
                total += load
        return total

    def _hash(self, key: str) -> int:
        """Hash a key to an integer value."""
        # Use MD5 for consistent hashing (could use xxhash for better performance)
        hash_obj = hashlib.md5(key.encode())
        # Use first 8 bytes as an integer
        return int(hash_obj.hexdigest()[:16], 16)

    def _search(self, key_hash: int) -> Tuple[int, int]:
        """Find the hash point and its index on the ring for a given key hash."""
        # Binary search for the first hash >= key_hash
        idx = bisect.bisect_left(self._sorted_hashes, key_hash)

        # If we're past the end, wrap around to the first hash
        if idx >= len(self._sorted_hashes):
            idx = 0

        return self._sorted_hashes[idx], idx

    def _add_replica_to_ring(self, replica_id: str):
        """Add a replica to the hash ring with virtual nodes."""
        for i in range(self._virtual_nodes):
            # Create a unique hash for each virtual node
            virtual_node_key = f"{replica_id}:{i}"
            hash_val = self._hash(virtual_node_key)

            # Add to hash ring
            self._hash_to_replica_id[hash_val] = replica_id
            bisect.insort(self._sorted_hashes, hash_val)

        logger.debug(f"Added replica {replica_id} to hash ring with {self._virtual_nodes} virtual nodes")

    def _remove_replica_from_ring(self, replica_id: str):
        """Remove a replica from the hash ring."""
        # Find all hash points for this replica
        hash_points_to_remove = []
        for hash_val, rid in self._hash_to_replica_id.items():
            if rid == replica_id:
                hash_points_to_remove.append(hash_val)

        # Remove from data structures
        for hash_val in hash_points_to_remove:
            del self._hash_to_replica_id[hash_val]
            idx = bisect.bisect_left(self._sorted_hashes, hash_val)
            if idx < len(self._sorted_hashes) and self._sorted_hashes[idx] == hash_val:
                self._sorted_hashes.pop(idx)

        logger.debug(f"Removed replica {replica_id} from hash ring")

    def create_replica_wrapper(
        self, replica_info: RunningReplicaInfo
    ) -> RunningReplica:
        """Create a new replica wrapper."""
        if self._create_replica_wrapper_func:
            return self._create_replica_wrapper_func(replica_info)
        return RunningReplica(replica_info)

    def update_replicas(self, replicas: List[RunningReplica]):
        """Update the list of available replicas."""
        # Track which replicas are no longer present
        old_replica_ids = set(self._replicas.keys())
        new_replica_ids = {r.replica_id for r in replicas}

        # Remove replicas that are no longer present
        for replica_id in old_replica_ids - new_replica_ids:
            self._remove_replica_from_ring(replica_id)

        # Add new replicas
        for replica in replicas:
            if replica.replica_id not in self._replicas:
                self._add_replica_to_ring(replica.replica_id)

        # Update replicas dictionary
        self._replicas = {replica.replica_id: replica for replica in replicas}

        logger.info(
            f"ConsistentHashScheduler: Updated replicas. Total: {len(self._replicas)}, "
            f"Hash ring size: {len(self._sorted_hashes)}"
        )

    def on_replica_actor_died(self, replica_id: ReplicaID):
        """Handle a replica that has died."""
        if replica_id in self._replicas:
            # Remove from replicas dictionary
            del self._replicas[replica_id]

            # Remove from hash ring
            self._remove_replica_from_ring(replica_id)

            logger.warning(
                f"ConsistentHashScheduler: Replica {replica_id} died. "
                f"Remaining: {len(self._replicas)}"
            )

    def on_replica_actor_unavailable(self, replica_id: ReplicaID):
        """Handle a replica that is unavailable."""
        # We don't remove the replica here, just invalidate the cache
        self._replica_queue_len_cache.invalidate_key(replica_id)
        logger.warning(f"ConsistentHashScheduler: Replica {replica_id} unavailable.")

    def on_new_queue_len_info(
        self, replica_id: ReplicaID, queue_len_info: ReplicaQueueLengthInfo
    ):
        """Update queue length cache with new info received from replica."""
        if self._use_replica_queue_len_cache:
            with self._load_lock:
                self._replica_queue_len_cache.update(
                    replica_id, queue_len_info.num_ongoing_requests
                )
                logger.debug(
                    f"ConsistentHashScheduler: Updated queue length for {replica_id}: "
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

    def _extract_cache_key(self, payload, request_id: str) -> str:
        """Extract cache key from OpenAI-compatible chat completions payload.

        For chat completions, we want to hash based on:
        1. System prompt (if present)
        2. First N user messages (configurable)

        This ensures that similar conversation contexts are routed to the same replica.
        """
        # Default fallback
        if not payload:
            logger.info(f"No payload found, using request_id: {request_id}")
            return str(request_id)

        try:
            # Handle different payload formats (args could be tuple, list, or dict)
            if isinstance(payload, (tuple, list)) and len(payload) > 0:
                # Assume first argument contains the request data
                request_data = payload[0]
            elif isinstance(payload, dict):
                request_data = payload
            else:
                # Fallback to string representation
                return str(payload)

            # Extract relevant fields for cache key
            cache_components = []

            # System prompt and user messages
            messages = request_data.get('messages', [])
            system_prompt = None
            user_messages = []

            for msg in messages:
                if isinstance(msg, dict):
                    role = msg.get('role', '')
                    content = msg.get('content', '')

                    if role == 'system':
                        system_prompt = content
                    elif role == 'user':
                        user_messages.append(content)
                        # Early exit when we have enough user messages
                        if len(user_messages) >= self._max_user_messages_for_cache:
                            break

            # Add system prompt to cache key
            if system_prompt:
                cache_components.append(f"system:{system_prompt}")

            # Add first N user messages (we already have at most N messages)
            for i, user_msg in enumerate(user_messages):
                cache_components.append(f"user_{i}:{user_msg}")

            # Join components
            if cache_components:
                cache_key = "|".join(cache_components)
                logger.debug(f"Extracted cache key: {cache_key[:100]}...")  # Log first 100 chars
                return cache_key
            else:
                # No recognizable chat completions format, fallback
                logger.info(f"No chat completions format detected, using payload string")
                return str(payload)

        except Exception as e:
            logger.warning(f"Error extracting cache key from payload: {e}, using request_id")
            return str(request_id)

    def on_request_completed(self, replica_id: ReplicaID):
        """Handle request completion for a replica.

        Called by the router when a request finishes processing.
        """
        with self._load_lock:
            if self._use_replica_queue_len_cache:
                current_load = self._replica_queue_len_cache.get(replica_id)
                if current_load is None:
                    logger.warning(
                        f"CHWBL: Attempted to decrement load for {replica_id} but no load info exists"
                    )
                    return

                new_load = max(0, current_load - 1)  # Ensure non-negative
                self._replica_queue_len_cache.update(replica_id, new_load)

                logger.debug(
                    f"CHWBL: Decremented load for {replica_id}: "
                    f"{current_load} -> {new_load}"
                )