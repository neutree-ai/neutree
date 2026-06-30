import os
import types

from serve._metrics.prometheus_multiproc import (
    ensure_prometheus_multiproc_dir,
    install_stable_prometheus_multiproc_dir,
)


def test_ensure_prometheus_multiproc_dir_creates_stable_pid_dir(
    monkeypatch, tmp_path
):
    monkeypatch.delenv("PROMETHEUS_MULTIPROC_DIR", raising=False)
    stale_file = tmp_path / "sglang" / str(os.getpid()) / "counter.db"
    stale_file.parent.mkdir(parents=True)
    stale_file.write_text("stale")

    path = ensure_prometheus_multiproc_dir(base_dir=str(tmp_path), namespace="sglang")

    assert path == stale_file.parent
    assert os.environ["PROMETHEUS_MULTIPROC_DIR"] == str(path)
    assert path.exists()
    assert list(path.iterdir()) == []


def test_ensure_prometheus_multiproc_dir_preserves_existing_env(
    monkeypatch, tmp_path
):
    existing = tmp_path / "custom"
    monkeypatch.setenv("PROMETHEUS_MULTIPROC_DIR", str(existing))

    path = ensure_prometheus_multiproc_dir(base_dir=str(tmp_path), namespace="sglang")

    assert path == existing
    assert existing.exists()
    assert os.environ["PROMETHEUS_MULTIPROC_DIR"] == str(existing)


def test_install_stable_prometheus_multiproc_dir_patches_common_and_engine(
    monkeypatch, tmp_path
):
    monkeypatch.delenv("PROMETHEUS_MULTIPROC_DIR", raising=False)
    common_module = types.SimpleNamespace(
        set_prometheus_multiproc_dir=lambda: "common-temp-dir"
    )
    engine_module = types.SimpleNamespace(
        set_prometheus_multiproc_dir=lambda: "engine-temp-dir"
    )

    path = install_stable_prometheus_multiproc_dir(
        common_module,
        engine_module,
        base_dir=str(tmp_path),
        namespace="sglang",
    )

    assert path == tmp_path / "sglang" / str(os.getpid())
    assert common_module.set_prometheus_multiproc_dir() == str(path)
    assert engine_module.set_prometheus_multiproc_dir() == str(path)
