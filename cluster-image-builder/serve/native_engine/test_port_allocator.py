from __future__ import annotations

import json

from serve.native_engine.port_allocator import LocalPortAllocator


def test_allocator_reserves_first_available_port_from_30000(tmp_path) -> None:
    allocator = LocalPortAllocator(
        directory=tmp_path,
        port_range_start=30000,
        port_range_end=30002,
        owner_id="replica-a",
        process_identity=lambda: (101, "start-a"),
        is_port_available=lambda port: port == 30001,
    )

    lease = allocator.acquire()

    assert lease.port == 30001
    assert json.loads((tmp_path / "allocated_ports.json").read_text()) == {
        "30001": {
            "owner_id": "replica-a",
            "pid": 101,
            "process_start_time": "start-a",
        }
    }


def test_allocator_keeps_port_locked_until_the_owner_releases_it(tmp_path) -> None:
    first = LocalPortAllocator(
        directory=tmp_path,
        port_range_start=30000,
        port_range_end=30000,
        owner_id="replica-a",
        process_identity=lambda: (999999, "start-a"),
        is_port_available=lambda _port: True,
    )
    lease = first.acquire()
    second = LocalPortAllocator(
        directory=tmp_path,
        port_range_start=30000,
        port_range_end=30000,
        owner_id="replica-b",
        process_identity=lambda: (202, "start-b"),
        is_port_available=lambda _port: True,
    )

    try:
        second.acquire()
    except RuntimeError as exc:
        assert "no available native engine ports" in str(exc)
    else:
        raise AssertionError("second actor acquired a live actor's port")
    first.release(lease)
    replacement = second.acquire()

    assert replacement.port == 30000
    assert json.loads((tmp_path / "allocated_ports.json").read_text())["30000"]["owner_id"] == "replica-b"


def test_allocator_does_not_release_another_actor_port(tmp_path) -> None:
    owner = LocalPortAllocator(
        directory=tmp_path,
        port_range_start=30000,
        port_range_end=30000,
        owner_id="replica-a",
        process_identity=lambda: (101, "start-a"),
        is_port_available=lambda _port: True,
    )
    lease = owner.acquire()
    other = LocalPortAllocator(
        directory=tmp_path,
        port_range_start=30000,
        port_range_end=30000,
        owner_id="replica-b",
        process_identity=lambda: (202, "start-b"),
        is_port_available=lambda _port: True,
    )

    other.release(lease)

    assert "30000" in json.loads((tmp_path / "allocated_ports.json").read_text())
