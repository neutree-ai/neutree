import logging
import os

from ray import serve
from vllm.v1.metrics.ray_wrappers import (
    RayPrometheusStatLogger,
    RaySpecDecodingProm,
    RayKVConnectorPrometheus,
    RayGaugeWrapper,
    RayCounterWrapper,
    RayHistogramWrapper,
)

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


class NeutreeRayStatLogger(RayPrometheusStatLogger):
    """RayPrometheusStatLogger with Ray Serve context labels injected.

    Transparently extends all vLLM metrics with deployment, replica,
    and application labels from the Ray Serve replica context.
    """

    def __init__(self, vllm_config, engine_indexes=None):
        extra_labels = {}
        try:
            ctx = serve.get_replica_context()
            extra_labels = {
                "deployment": ctx.deployment,
                "replica": ctx.replica_tag,
                "engine": os.environ.get("ENGINE_NAME", "unknown"),
                "engine_version": os.environ.get("ENGINE_VERSION", "unknown"),
            }
            if hasattr(ctx, "app_name"):
                extra_labels["application"] = ctx.app_name
        except Exception as exc:  # noqa: BLE001
            # Two real-world miss-paths, both treated identically (skip labels):
            #   - RuntimeError: legacy ray (< 2.53) raise path
            #   - ray.serve.exceptions.RayServeException: 2.53+ raise path
            # Also covers PD same-host: PrefillActor / DecodeActor run as
            # plain @ray.remote actors (not Serve deployments) so calling
            # get_replica_context() from inside vLLM's stat logger factory
            # — which fires during super().__init__ before our subclass body
            # gets a chance — always misses. Custom labels then come from
            # the outer PDCollocatedBackend (a Serve deployment) reporting
            # its own context separately; vLLM-side metrics are fine
            # unlabeled.
            logger.warning(
                "NeutreeRayStatLogger: not running in Ray Serve context "
                "(%s); skipping custom labels",
                type(exc).__name__,
            )

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
