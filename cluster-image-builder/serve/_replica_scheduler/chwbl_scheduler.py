import hashlib
import logging
import bisect
import threading
from typing import Dict, List, Set, Tuple, Optional

from ray.serve._private.common import ReplicaID, ReplicaQueueLengthInfo
from ray.serve._private.constants import SERVE_LOGGER_NAME
from ray.serve._private.request_router.common import (
    PendingRequest,
)
from ray.serve._private.request_router.request_router import RequestRouter
from ray.serve._private.request_router.replica_wrapper import RunningReplica

logger = logging.getLogger(SERVE_LOGGER_NAME)


class ConsistentHashReplicaScheduler(RequestRouter):
    """A scheduler that routes requests using consistent hashing with bounded loads.

    This scheduler ensures that similar payloads are routed to the same replica
    while maintaining load balance and minimizing disruption when replicas are added or removed.

    The implementation uses Consistent Hashing with Bounded Loads (CHWBL) to balance
    between consistent routing and load distribution.
    """

    def __init__(
        self,
        *args,
        virtual_nodes_per_replica: int = 100,
        load_factor: float = 1.25,
        max_user_messages_for_cache: int = 2,
        **kwargs,
    ):
        # Pass all arguments to parent RequestRouter class
        super().__init__(*args, **kwargs)

        # Consistent hashing settings
        self._virtual_nodes = virtual_nodes_per_replica
        self._load_factor = load_factor

        # Cache key extraction settings for chat completions
        self._max_user_messages_for_cache = max_user_messages_for_cache

        # Hash ring data structures
        self._hash_to_replica_id: Dict[int, ReplicaID] = {}  # Maps hash points to replica IDs
        self._sorted_hashes: List[int] = []  # Sorted list of hash points for binary search

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

    async def choose_replicas(
        self,
        candidate_replicas: List[RunningReplica],
        pending_request: Optional[PendingRequest] = None,
    ) -> List[List[RunningReplica]]:
        """Choose replicas based on consistent hashing with bounded load.

        Args:
            candidate_replicas: List of available replicas to choose from
            pending_request: The pending request to route (optional)

        Returns:
            List of replica lists ordered by priority. Each inner list contains
            replicas of equal priority. The router will try replicas in order.
        """
        if not candidate_replicas:
            logger.warning("No candidate replicas available for consistent hash scheduling")
            return [[]]

        # Build a map of candidate replicas for quick lookup
        candidate_map = {r.replica_id: r for r in candidate_replicas}

        # If no pending request, return all candidates as equal priority
        if pending_request is None:
            return [candidate_replicas]

        # Extract the payload from the PendingRequest
        payload = pending_request.args
        request_id = pending_request.metadata.request_id

        # Extract cache key for OpenAI-compatible chat completions
        cache_key = self._extract_cache_key(payload, request_id)

        # Calculate a hash of the cache key
        payload_hash = self._hash(cache_key)

        # If hash ring is empty, return candidates as-is
        if len(self._sorted_hashes) == 0:
            return [candidate_replicas]

        # Find initial replica using consistent hashing
        replica_hash, replica_idx = self._search(payload_hash)
        initial_replica_id = self._hash_to_replica_id.get(replica_hash)

        if initial_replica_id is None:
            return [candidate_replicas]

        logger.debug(
            f"CHWBL: Initial lookup for payload hash {payload_hash} -> "
            f"replica {initial_replica_id}"
        )

        # Create load snapshot at the beginning to ensure consistency
        load_snapshot = self._create_load_snapshot()

        # Build priority-ordered list of replicas
        result: List[List[RunningReplica]] = []
        checked_replica_ids: Set[ReplicaID] = set()

        # Start from the initial replica and order by consistent hash ring position
        current_idx = replica_idx
        iterations = 0
        max_iterations = len(self._sorted_hashes)

        while len(checked_replica_ids) < len(candidate_map) and iterations < max_iterations:
            iterations += 1

            current_hash = self._sorted_hashes[current_idx]
            current_replica_id = self._hash_to_replica_id.get(current_hash)

            if current_replica_id is None:
                current_idx = (current_idx + 1) % len(self._sorted_hashes)
                continue

            # Skip duplicate replica IDs (from virtual nodes) and non-candidates
            if current_replica_id in checked_replica_ids or current_replica_id not in candidate_map:
                current_idx = (current_idx + 1) % len(self._sorted_hashes)
                continue

            checked_replica_ids.add(current_replica_id)

            # Get the replica from candidates
            replica = candidate_map[current_replica_id]

            # Check if this replica meets the load constraints
            if self._check_load_with_snapshot(current_replica_id, load_snapshot, len(candidate_map)):
                # High priority - meets load factor
                result.append([replica])
                logger.debug(
                    f"CHWBL: Adding replica {current_replica_id} as high priority, "
                    f"load={load_snapshot.get(current_replica_id, 0)}"
                )
            else:
                # Lower priority - exceeds load factor but still usable
                result.append([replica])
                logger.debug(
                    f"CHWBL: Adding replica {current_replica_id} as lower priority (overloaded), "
                    f"load={load_snapshot.get(current_replica_id, 0)}"
                )

            current_idx = (current_idx + 1) % len(self._sorted_hashes)

        # Add any remaining candidates that weren't in the hash ring
        for replica_id, replica in candidate_map.items():
            if replica_id not in checked_replica_ids:
                result.append([replica])

        if result:
            first_choice = result[0][0].replica_id if result[0] else 'none'
            logger.info(
                f"CHWBL: Ordered {len(result)} replicas for payload hash {payload_hash}, "
                f"first choice: {first_choice}"
            )
        else:
            # Fallback: return all candidates
            result = [[r] for r in candidate_replicas]

        return result

    def _check_load_with_snapshot(
        self, replica_id: ReplicaID, load_snapshot: Dict[ReplicaID, int], num_replicas: int
    ) -> bool:
        """Check if the replica meets the load constraints using a load snapshot.

        Args:
            replica_id: The replica to check
            load_snapshot: Pre-captured snapshot of all replica loads
            num_replicas: Number of replicas for calculating average

        Returns:
            True if the replica can accept the request, False otherwise
        """
        load = load_snapshot.get(replica_id, 0)

        if num_replicas == 0:
            return True

        # Calculate average load across all replicas using snapshot
        total_load = sum(load_snapshot.values())
        avg_load = (total_load + 1) / num_replicas  # +1 for the current request

        # Apply load factor threshold
        threshold = avg_load * self._load_factor

        logger.debug(
            f"CHWBL: Replica {replica_id} load={load}, "
            f"total_load={total_load}, avg_load={avg_load:.2f}, threshold={threshold:.2f}"
        )

        # Check if this replica is under the threshold (including the current request)
        return (load + 1) <= threshold

    def _hash(self, key: str) -> int:
        """Hash a key to an integer value."""
        hash_obj = hashlib.md5(key.encode())
        return int(hash_obj.hexdigest()[:16], 16)

    def _search(self, key_hash: int) -> Tuple[int, int]:
        """Find the hash point and its index on the ring for a given key hash."""
        idx = bisect.bisect_left(self._sorted_hashes, key_hash)
        if idx >= len(self._sorted_hashes):
            idx = 0
        return self._sorted_hashes[idx], idx

    def _add_replica_to_ring(self, replica_id: ReplicaID):
        """Add a replica to the hash ring with virtual nodes."""
        replica_id_str = str(replica_id)
        for i in range(self._virtual_nodes):
            virtual_node_key = f"{replica_id_str}:{i}"
            hash_val = self._hash(virtual_node_key)
            self._hash_to_replica_id[hash_val] = replica_id
            bisect.insort(self._sorted_hashes, hash_val)
        logger.debug(f"Added replica {replica_id} to hash ring with {self._virtual_nodes} virtual nodes")

    def _remove_replica_from_ring(self, replica_id: ReplicaID):
        """Remove a replica from the hash ring."""
        hash_points_to_remove = [
            hash_val for hash_val, rid in self._hash_to_replica_id.items()
            if rid == replica_id
        ]
        for hash_val in hash_points_to_remove:
            del self._hash_to_replica_id[hash_val]
            idx = bisect.bisect_left(self._sorted_hashes, hash_val)
            if idx < len(self._sorted_hashes) and self._sorted_hashes[idx] == hash_val:
                self._sorted_hashes.pop(idx)
        logger.debug(f"Removed replica {replica_id} from hash ring")

    def update_replicas(self, replicas: List[RunningReplica]):
        """Update the list of available replicas and maintain hash ring."""
        # Track which replicas are no longer present
        old_replica_ids = set(self._replicas.keys())
        new_replica_ids = {r.replica_id for r in replicas}

        # Remove replicas that are no longer present from hash ring
        for replica_id in old_replica_ids - new_replica_ids:
            self._remove_replica_from_ring(replica_id)

        # Add new replicas to hash ring
        for replica in replicas:
            if replica.replica_id not in self._replicas:
                self._add_replica_to_ring(replica.replica_id)

        # Call parent's update_replicas to handle the rest
        super().update_replicas(replicas)

        logger.info(
            f"ConsistentHashScheduler: Updated replicas. Total: {len(self._replicas)}, "
            f"Hash ring size: {len(self._sorted_hashes)}"
        )

    def on_replica_actor_died(self, replica_id: ReplicaID):
        """Handle a replica that has died."""
        # Remove from hash ring
        self._remove_replica_from_ring(replica_id)
        # Call parent's handler
        super().on_replica_actor_died(replica_id)
        logger.warning(
            f"ConsistentHashScheduler: Replica {replica_id} died. "
            f"Remaining: {len(self._replicas)}"
        )

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
        if not payload:
            logger.info(f"No payload found, using request_id: {request_id}")
            return str(request_id)

        try:
            # Handle different payload formats (args could be tuple, list, or dict)
            if isinstance(payload, (tuple, list)) and len(payload) > 0:
                request_data = payload[0]
            elif isinstance(payload, dict):
                request_data = payload
            else:
                return str(payload)

            cache_components = []
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
                        if len(user_messages) >= self._max_user_messages_for_cache:
                            break

            if system_prompt:
                cache_components.append(f"system:{system_prompt}")

            for i, user_msg in enumerate(user_messages):
                cache_components.append(f"user_{i}:{user_msg}")

            if cache_components:
                cache_key = "|".join(cache_components)
                logger.debug(f"Extracted cache key: {cache_key[:100]}...")
                return cache_key
            else:
                logger.info(f"No chat completions format detected, using payload string")
                return str(payload)

        except Exception as e:
            logger.warning(f"Error extracting cache key from payload: {e}, using request_id")
            return str(request_id)
