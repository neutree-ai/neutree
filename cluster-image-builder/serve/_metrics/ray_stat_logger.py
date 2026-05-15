import logging
import os

from ray import serve
from vllm.v1.metrics.ray_wrappers import (
    RayPrometheusStatLogger,
    RaySpecDecodingProm,
    RayGaugeWrapper,
    RayCounterWrapper,
    RayHistogramWrapper,
)

# v0.19.0 renamed RayKVConnectorPrometheus -> RayKVConnectorProm. Probe both
# so a single image source supports the v0.17.x and v0.19.x+ engine bases.
try:
    from vllm.v1.metrics.ray_wrappers import RayKVConnectorProm as RayKVConnectorPrometheus
except ImportError:
    from vllm.v1.metrics.ray_wrappers import RayKVConnectorPrometheus

logger = logging.getLogger("ray.serve")


def _make_extended_metric_cls(base_cls, extra_labels):
    """Create a metric wrapper that transparently extends labelnames."""

    class Extended(base_cls):
        def __init__(self, name, documentation=None, labelnames=None, **kwargs):
            extended_names = list(labelnames or []) + list(extra_labels.keys())
            super().__init__(name=name, documentation=documentation,
                             labelnames=extended_names, **kwargs)

        def labels(self, *args, **kwargs):
            if args:
                args = args + tuple(extra_labels.values())
            if kwargs:
                kwargs.update(extra_labels)
            return super().labels(*args, **kwargs)

    return Extended


def _make_extended_spec_decoding_cls(base_cls, extra_labels):
    """Extend SpecDecodingProm with custom labels via its _counter_cls."""

    class Extended(base_cls):
        _counter_cls = _make_extended_metric_cls(RayCounterWrapper, extra_labels)

    return Extended


def _make_extended_kv_connector_cls(base_cls, extra_labels):
    """Extend KVConnectorPrometheus with custom labels via its _cls vars."""

    class Extended(base_cls):
        _gauge_cls = _make_extended_metric_cls(RayGaugeWrapper, extra_labels)
        _counter_cls = _make_extended_metric_cls(RayCounterWrapper, extra_labels)
        _histogram_cls = _make_extended_metric_cls(RayHistogramWrapper, extra_labels)

    return Extended


# Env-var fallback for the Serve-context label triple. PD same-host inner
# actors (PrefillActor / DecodeActor) are plain @ray.remote actors, not
# Serve deployments, so `serve.get_replica_context()` raises there. The
# outer PDCollocatedBackend (which IS a Serve deployment) captures its own
# context and forwards it via these env vars at actor spawn time so the
# label schema stays identical across monolithic and PD paths.
_ENV_DEPLOYMENT = "NEUTREE_RAY_STAT_DEPLOYMENT"
_ENV_REPLICA = "NEUTREE_RAY_STAT_REPLICA"
_ENV_APPLICATION = "NEUTREE_RAY_STAT_APPLICATION"
# PD-only extra dimensions. Empty / unset for monolithic so its label
# schema is unchanged. Adding labels with non-empty values is
# Prometheus-non-breaking — existing queries match by subset.
_ENV_ROLE = "NEUTREE_RAY_STAT_ROLE"
_ENV_RANK = "NEUTREE_RAY_STAT_RANK"


def _resolve_extra_labels():
    """Return the label triple {deployment, replica, application} plus
    engine identity (and PD-only role/rank), pulled from Serve context
    when available, env vars otherwise. Empty values are dropped so the
    metric labelnames list only carries dimensions we actually populate.
    """
    labels = {}
    try:
        ctx = serve.get_replica_context()
        labels["deployment"] = ctx.deployment
        labels["replica"] = ctx.replica_tag
        if hasattr(ctx, "app_name"):
            labels["application"] = ctx.app_name
    except Exception as exc:  # noqa: BLE001
        # Two miss-paths, both treated identically (fall back to env):
        #   - RuntimeError: legacy ray (< 2.53) raise path
        #   - ray.serve.exceptions.RayServeException: 2.53+ raise path
        # PD inner actors land here every time; env fallback below preserves
        # the label schema.
        logger.info(
            "NeutreeRayStatLogger: no Ray Serve context (%s); falling back "
            "to env-var labels", type(exc).__name__,
        )
        labels["deployment"] = os.environ.get(_ENV_DEPLOYMENT, "")
        labels["replica"] = os.environ.get(_ENV_REPLICA, "")
        labels["application"] = os.environ.get(_ENV_APPLICATION, "")

    labels["engine"] = os.environ.get("ENGINE_NAME", "unknown")
    labels["engine_version"] = os.environ.get("ENGINE_VERSION", "unknown")
    role = os.environ.get(_ENV_ROLE, "")
    if role:
        labels["role"] = role
        rank = os.environ.get(_ENV_RANK, "")
        if rank:
            labels["rank"] = rank
    return {k: v for k, v in labels.items() if v}


class NeutreeRayStatLogger(RayPrometheusStatLogger):
    """RayPrometheusStatLogger with Ray Serve context labels injected.

    Transparently extends all vLLM metrics with deployment, replica,
    and application labels from the Ray Serve replica context. PD inner
    actors (no Serve context) read the same triple from env vars set by
    the outer PDCollocatedBackend, plus role/rank for per-actor slicing.
    """

    def __init__(self, vllm_config, engine_indexes=None):
        extra_labels = _resolve_extra_labels()

        if extra_labels:
            self._gauge_cls = _make_extended_metric_cls(
                RayGaugeWrapper, extra_labels)
            self._counter_cls = _make_extended_metric_cls(
                RayCounterWrapper, extra_labels)
            self._histogram_cls = _make_extended_metric_cls(
                RayHistogramWrapper, extra_labels)
            self._spec_decoding_cls = _make_extended_spec_decoding_cls(
                RaySpecDecodingProm, extra_labels)
            self._kv_connector_cls = _make_extended_kv_connector_cls(
                RayKVConnectorPrometheus, extra_labels)

        super().__init__(vllm_config, engine_indexes)

        logger.info(
            f"NeutreeRayStatLogger initialized with extra labels: "
            f"{list(extra_labels.keys())}"
        )
