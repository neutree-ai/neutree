"""Allocate host-network engine ports through a node-local shared directory."""

from __future__ import annotations

import fcntl
import json
import os
import socket
import tempfile
from collections.abc import Callable
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import Any, TextIO

_DEFAULT_DIRECTORY = "/var/run/neutree/ports"
_DEFAULT_PORT_RANGE_START = 30000
_DEFAULT_PORT_RANGE_END = 32767


@dataclass(frozen=True)
class PortLease:
    """A port reservation that belongs to exactly one Ray Serve replica."""

    port: int
    owner_id: str
    pid: int
    process_start_time: str


class LocalPortAllocator:
    """Coordinate port choices between host-network engine containers.

    The directory must be a host bind mount shared by every backend container
    on a node. ``flock`` serializes Neutree allocations; the bind probe also
    excludes listeners that do not participate in the allocator.
    """

    def __init__(
        self,
        *,
        directory: Path | str = _DEFAULT_DIRECTORY,
        port_range_start: int = _DEFAULT_PORT_RANGE_START,
        port_range_end: int = _DEFAULT_PORT_RANGE_END,
        owner_id: str,
        process_identity: Callable[[], tuple[int, str]] | None = None,
        is_port_available: Callable[[int], bool] | None = None,
    ) -> None:
        if port_range_start < 1 or port_range_end > 65535 or port_range_start > port_range_end:
            raise ValueError("invalid native engine port range")

        self._directory = Path(directory)
        self._start = port_range_start
        self._end = port_range_end
        self._owner_id = owner_id
        self._process_identity = process_identity or _current_process_identity
        self._is_port_available = is_port_available or is_port_available_on_loopback
        self._port_locks: dict[int, TextIO] = {}

    def acquire(self) -> PortLease:
        """Reserve the first available port in the configured range."""
        owner = self._owner()
        with self._locked_state() as allocations:
            for port in range(self._start, self._end + 1):
                port_key = str(port)
                if port_key in allocations:
                    if not self._take_port_lock(port):
                        continue
                    if not self._is_port_available(port):
                        self._release_port_lock(port)
                        continue
                    del allocations[port_key]
                if not self._is_port_available(port) or not self._take_port_lock(port):
                    continue
                allocations[port_key] = owner
                self._write_state(allocations)
                return PortLease(port=port, **owner)
        raise RuntimeError(f"no available native engine ports in range {self._start}-{self._end}")

    def release(self, lease: PortLease) -> None:
        """Release a reservation only when this actor still owns it."""
        owner = self._owner()
        with self._locked_state() as allocations:
            port_key = str(lease.port)
            if allocations.get(port_key) == owner:
                del allocations[port_key]
                self._write_state(allocations)
            self._release_port_lock(lease.port)

    def _owner(self) -> dict[str, Any]:
        pid, process_start_time = self._process_identity()
        return {
            "owner_id": self._owner_id,
            "pid": pid,
            "process_start_time": process_start_time,
        }

    def _take_port_lock(self, port: int) -> bool:
        if port in self._port_locks:
            return True
        lock_file = (self._directory / f"port-{port}.lock").open("a+")
        try:
            fcntl.flock(lock_file.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            lock_file.close()
            return False
        self._port_locks[port] = lock_file
        return True

    def _release_port_lock(self, port: int) -> None:
        lock_file = self._port_locks.pop(port, None)
        if lock_file is None:
            return
        fcntl.flock(lock_file.fileno(), fcntl.LOCK_UN)
        lock_file.close()

    @contextmanager
    def _locked_state(self):
        self._directory.mkdir(mode=0o1777, parents=True, exist_ok=True)
        lock_path = self._directory / "port.lock"
        lock_file = lock_path.open("a+")
        fcntl.flock(lock_file.fileno(), fcntl.LOCK_EX)
        try:
            yield self._read_state()
        finally:
            fcntl.flock(lock_file.fileno(), fcntl.LOCK_UN)
            lock_file.close()

    def _read_state(self) -> dict[str, dict[str, Any]]:
        state_path = self._directory / "allocated_ports.json"
        if not state_path.exists():
            return {}
        try:
            state = json.loads(state_path.read_text())
        except json.JSONDecodeError as exc:
            raise RuntimeError(f"invalid native engine port state: {state_path}") from exc
        if not isinstance(state, dict):
            raise RuntimeError(f"invalid native engine port state: {state_path}")
        return state

    def _write_state(self, allocations: dict[str, dict[str, Any]]) -> None:
        state_path = self._directory / "allocated_ports.json"
        with tempfile.NamedTemporaryFile(mode="w", dir=self._directory, delete=False) as state_file:
            json.dump(allocations, state_file, sort_keys=True)
            state_file.flush()
            os.fsync(state_file.fileno())
            temporary_path = Path(state_file.name)
        os.replace(temporary_path, state_path)


def _current_process_identity() -> tuple[int, str]:
    pid = os.getpid()
    return pid, _process_start_time(pid)


def _process_start_time(pid: int) -> str:
    try:
        return Path(f"/proc/{pid}/stat").read_text().split()[21]
    except (FileNotFoundError, IndexError, OSError):
        return ""


def is_port_available_on_loopback(port: int) -> bool:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 0)
        try:
            sock.bind(("127.0.0.1", port))
        except OSError:
            return False
    return True
