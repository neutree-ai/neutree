"""Replica-local KV event consumer and block index.

The index deliberately computes a Neutree-owned fingerprint from the token
blocks reported by vLLM. This lets the request router match tokenized requests
without depending on vLLM's internal block-hash implementation.
"""

from __future__ import annotations

import hashlib
import json
import logging
import struct
import threading
import time
from dataclasses import dataclass
from typing import Any, Optional, Sequence


logger = logging.getLogger(__name__)

KV_ROUTING_STATS_KEY = "neutree_kv_cache"
KV_ROUTING_METADATA_KEY = "_neutree_kv_routing"
KV_ROUTING_STATS_VERSION = 1

_ROOT_CHAIN_SEED = b"neutree-kv-token-chain-v1"


def build_local_kv_events_config(
    existing: Any,
    *,
    endpoint: str,
    topic: str,
) -> dict[str, Any]:
    """Build the vLLM event config required by a replica-local subscriber."""
    if isinstance(existing, str):
        try:
            existing = json.loads(existing)
        except (json.JSONDecodeError, TypeError):
            existing = None
    config = dict(existing) if isinstance(existing, dict) else {}
    config.update(
        {
            "enable_kv_cache_events": True,
            "publisher": "zmq",
            "endpoint": endpoint,
            "replay_endpoint": None,
            "topic": topic,
        }
    )
    return config


def build_native_cpu_offload_config(existing: Any) -> dict[str, Any]:
    """Make native CPU offload events include block tokens and hashes."""
    if isinstance(existing, str):
        try:
            existing = json.loads(existing)
        except (json.JSONDecodeError, TypeError):
            existing = None
    config = dict(existing) if isinstance(existing, dict) else {}
    extra = config.get("kv_connector_extra_config")
    extra = dict(extra) if isinstance(extra, dict) else {}
    extra["self_describing_kv_events"] = True
    config["kv_connector_extra_config"] = extra
    return config


def _next_token_block_fingerprint(
    parent_fingerprint: bytes,
    token_ids: Sequence[int],
) -> bytes:
    try:
        token_bytes = struct.pack(f"!{len(token_ids)}I", *token_ids)
    except (struct.error, TypeError) as exc:
        raise ValueError("token IDs must be unsigned 32-bit integers") from exc
    return hashlib.sha256(parent_fingerprint + token_bytes).digest()


def token_block_fingerprints(
    token_ids: Sequence[int],
    block_size: int,
) -> list[str]:
    """Return deterministic fingerprints for every complete prefix block."""
    if (
        not isinstance(block_size, int)
        or isinstance(block_size, bool)
        or block_size <= 0
    ):
        return []

    fingerprints: list[str] = []
    parent = _ROOT_CHAIN_SEED
    for start in range(0, len(token_ids), block_size):
        block = token_ids[start : start + block_size]
        if len(block) < block_size:
            break
        parent = _next_token_block_fingerprint(parent, block)
        fingerprints.append(parent.hex())
    return fingerprints


def match_prefix_blocks(
    token_ids: Sequence[int],
    routing_stats: Any,
    *,
    now: Optional[float] = None,
    max_age_s: float = 5.0,
) -> int:
    """Return the number of consecutive prompt blocks resident on a replica."""
    if not isinstance(routing_stats, dict):
        return 0
    stats = routing_stats.get(KV_ROUTING_STATS_KEY)
    if not isinstance(stats, dict):
        return 0
    if stats.get("version") != KV_ROUTING_STATS_VERSION:
        return 0
    if not stats.get("ready") or not stats.get("trusted"):
        return 0
    if stats.get("consumer_alive") is not True:
        return 0

    captured_at = stats.get("captured_at")
    if not isinstance(captured_at, (int, float)) or isinstance(captured_at, bool):
        return 0
    current_time = time.time() if now is None else now
    if current_time < captured_at or current_time - captured_at > max_age_s:
        return 0

    block_size = stats.get("block_size")
    if (
        not isinstance(block_size, int)
        or isinstance(block_size, bool)
        or block_size <= 0
    ):
        return 0
    resident = stats.get("block_fingerprints")
    if not isinstance(resident, (list, tuple, set)):
        return 0
    resident_set = {item for item in resident if isinstance(item, str)}

    matched = 0
    for fingerprint in token_block_fingerprints(token_ids, block_size):
        if fingerprint not in resident_set:
            break
        matched += 1
    return matched


def _normalize_block_hash(value: Any) -> Any:
    if isinstance(value, bytearray):
        return bytes(value)
    if isinstance(value, memoryview):
        return value.tobytes()
    return value


@dataclass
class _BlockRecord:
    fingerprint: bytes
    block_size: int
    references: int
    depth: int
    order: int


class LocalKVBlockIndex:
    """Thread-safe index built from one vLLM replica's KV event stream."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._records: dict[Any, _BlockRecord] = {}
        self._last_sequence = -1
        self._last_event_at: Optional[float] = None
        self._ready = False
        self._trusted = True
        self._sequence_gaps = 0
        self._unsupported_events = 0
        self._event_batches = 0
        self._next_order = 0

    def apply_batch(self, sequence: int, event_batch: Any) -> None:
        """Apply one decoded vLLM KV event batch."""
        if not isinstance(sequence, int) or sequence < 0:
            return

        with self._lock:
            if sequence <= self._last_sequence:
                return

            expected = self._last_sequence + 1
            if sequence != expected:
                self._records.clear()
                self._trusted = False
                self._sequence_gaps += sequence - expected

            events = getattr(event_batch, "events", None)
            if not isinstance(events, list):
                self._trusted = False
                self._unsupported_events += 1
                events = []

            for event in events:
                event_type = type(event).__name__
                if event_type == "AllBlocksCleared":
                    self._records.clear()
                    self._trusted = True
                elif not self._trusted:
                    continue
                elif event_type == "BlockStored":
                    self._apply_stored(event)
                elif event_type == "BlockRemoved":
                    self._apply_removed(event)
                else:
                    self._unsupported_events += 1

            self._last_sequence = sequence
            self._last_event_at = time.time()
            self._event_batches += 1
            self._ready = True

    def _apply_stored(self, event: Any) -> None:
        block_hashes = getattr(event, "block_hashes", None)
        token_ids = getattr(event, "token_ids", None)
        block_size = getattr(event, "block_size", None)
        if not isinstance(block_hashes, list) or not isinstance(token_ids, list):
            self._unsupported_events += 1
            return
        if not isinstance(block_size, int) or block_size <= 0:
            self._unsupported_events += 1
            return
        if len(token_ids) != len(block_hashes) * block_size:
            self._unsupported_events += 1
            return

        parent_hash = _normalize_block_hash(getattr(event, "parent_block_hash", None))
        if parent_hash is None:
            parent_fingerprint = _ROOT_CHAIN_SEED
            parent_depth = 0
        else:
            parent_record = self._records.get(parent_hash)
            if parent_record is None:
                self._unsupported_events += 1
                return
            parent_fingerprint = parent_record.fingerprint
            parent_depth = parent_record.depth

        for index, block_hash in enumerate(block_hashes):
            start = index * block_size
            tokens = token_ids[start : start + block_size]
            fingerprint = _next_token_block_fingerprint(parent_fingerprint, tokens)
            key = _normalize_block_hash(block_hash)
            current = self._records.get(key)
            if current is not None:
                if current.fingerprint != fingerprint or current.block_size != block_size:
                    self._records.clear()
                    self._trusted = False
                    self._unsupported_events += 1
                    return
                current.references += 1
                current.order = self._next_order
            else:
                self._records[key] = _BlockRecord(
                    fingerprint=fingerprint,
                    block_size=block_size,
                    references=1,
                    depth=parent_depth + 1,
                    order=self._next_order,
                )
            self._next_order += 1
            parent_fingerprint = fingerprint
            parent_depth += 1

    def _apply_removed(self, event: Any) -> None:
        block_hashes = getattr(event, "block_hashes", None)
        if not isinstance(block_hashes, list):
            self._unsupported_events += 1
            return
        for block_hash in block_hashes:
            key = _normalize_block_hash(block_hash)
            current = self._records.get(key)
            if current is None:
                continue
            current.references -= 1
            if current.references <= 0:
                del self._records[key]

    def snapshot(self, max_fingerprints: int = 8192) -> dict[str, Any]:
        """Return a bounded, Ray-serializable routing snapshot."""
        try:
            max_fingerprints = max(0, int(max_fingerprints))
        except (TypeError, ValueError):
            max_fingerprints = 0
        with self._lock:
            block_sizes: set[int] = set()
            records: dict[bytes, _BlockRecord] = {}
            for record in self._records.values():
                records[record.fingerprint] = record
                block_sizes.add(record.block_size)

            supported_layout = len(block_sizes) <= 1
            ordered = sorted(
                records,
                key=lambda fingerprint: (
                    records[fingerprint].depth,
                    -records[fingerprint].order,
                ),
            )
            published = ordered[:max_fingerprints]

            return {
                "version": KV_ROUTING_STATS_VERSION,
                "ready": self._ready,
                "trusted": self._trusted and supported_layout,
                "block_size": next(iter(block_sizes)) if len(block_sizes) == 1 else None,
                "block_count": len(records),
                "published_block_count": len(published),
                "block_fingerprints": [item.hex() for item in published],
                "last_sequence": self._last_sequence,
                "last_event_at": self._last_event_at,
                "sequence_gaps": self._sequence_gaps,
                "unsupported_events": self._unsupported_events,
                "event_batches": self._event_batches,
            }


class LocalKVEventSubscriber:
    """Background ZMQ subscriber for a single replica-local vLLM publisher."""

    def __init__(
        self,
        endpoint: str,
        topic: str,
        index: LocalKVBlockIndex,
        *,
        poll_timeout_ms: int = 100,
    ) -> None:
        self._endpoint = endpoint
        self._topic = topic
        self._index = index
        self._poll_timeout_ms = poll_timeout_ms
        self._stop = threading.Event()
        self._started = threading.Event()
        self._error: Optional[str] = None
        self._thread = threading.Thread(
            target=self._run,
            daemon=True,
            name="neutree-kv-event-subscriber",
        )

    @property
    def is_alive(self) -> bool:
        return self._thread.is_alive() and self._error is None

    @property
    def error(self) -> Optional[str]:
        return self._error

    def start(self, timeout_s: float = 2.0) -> None:
        self._thread.start()
        self._started.wait(timeout=timeout_s)
        if self._error is not None:
            raise RuntimeError(self._error)
        if not self._started.is_set():
            raise TimeoutError("KV event subscriber did not start in time")

    def close(self, timeout_s: float = 2.0) -> None:
        self._stop.set()
        if self._thread.is_alive():
            self._thread.join(timeout=timeout_s)

    def _run(self) -> None:
        socket = None
        try:
            import msgspec
            import zmq
            from vllm.distributed.kv_events import KVEventBatch

            decoder = msgspec.msgpack.Decoder(type=KVEventBatch)
            context = zmq.Context.instance()
            socket = context.socket(zmq.SUB)
            socket.setsockopt(zmq.LINGER, 0)
            socket.setsockopt(zmq.SUBSCRIBE, self._topic.encode("utf-8"))
            socket.connect(self._endpoint)
            self._started.set()

            while not self._stop.is_set():
                if not socket.poll(self._poll_timeout_ms):
                    continue
                frames = socket.recv_multipart()
                if len(frames) != 3:
                    logger.warning(
                        "Ignoring malformed KV event message with %d frames",
                        len(frames),
                    )
                    continue
                _, sequence_bytes, payload = frames
                sequence = int.from_bytes(sequence_bytes, "big")
                self._index.apply_batch(sequence, decoder.decode(payload))
        except Exception as exc:
            self._error = f"KV event subscriber failed: {exc}"
            logger.exception(self._error)
            self._started.set()
        finally:
            if socket is not None:
                socket.close(linger=0)
