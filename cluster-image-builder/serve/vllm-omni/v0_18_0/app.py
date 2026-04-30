"""Ray Serve application for vLLM-Omni v0.18.0.

This is the Phase 1 skeleton. Per the design doc §5.4b, the full
``AsyncOmni`` integration is driven by the build-test loop:

  build engine image -> ssh push -> deploy endpoint
    -> tail Ray Serve logs -> fix shape mismatches -> repeat

The skeleton intentionally mirrors ``cluster-image-builder/serve/vllm/v0_17_1/app.py``
in shape so that diff-based review can spot exactly where vLLM-Omni
diverges. The known divergence points are marked ``OMNI-TODO`` and
will be filled in during build-test iteration:

* ``OMNI-TODO[entry-point]``: replace ``vllm.v1.engine.async_llm.AsyncLLM``
  with ``vllm_omni.entrypoints.AsyncOmni``; the constructor signature
  is verified against vllm-omni v0.18.0 source.
* ``OMNI-TODO[serving-class]``: vllm-omni bundles its own OpenAI
  handler in ``vllm_omni/entrypoints/openai/`` — confirm whether to
  reuse it or wire the chat completion endpoint manually.
* ``OMNI-TODO[output-shape]``: ``OmniRequestOutput`` carries
  text/audio/image latents in separate fields; the streaming response
  must serialize each modality according to the request's
  ``modalities`` value. Reference: vllm-omni
  ``examples/online_serving/qwen2_5_omni/`` upstream.
* ``OMNI-TODO[error-response]``: verify ``ErrorResponse`` is the
  nested-shape variant (v0.10.1+) and import path is
  ``vllm_omni.entrypoints.openai.protocol`` (mirror vLLM v0.17.1
  changes — see generate-engine-version skill Step 3c).

Phase 1 explicitly excludes:

* multi-stage cross-pod orchestration (use single-host
  ``worker_backend=multi_process`` only — Phase 3)
* TTS-only ``/v1/audio/speech``, image generation
  ``/v1/images/generations``, realtime WebSocket ``/v1/realtime``
  (Phase 4)

Phase 1 canary: ``Qwen/Qwen2.5-Omni-7B`` — single GPU, audio input
plus text+audio output via OpenAI-compatible chat completions.
"""

# OMNI-TODO[entry-point]: import AsyncOmni once skeleton becomes the
# build-test seed. The Dockerfile's pip stack guarantees vllm_omni is
# importable; the actual constructor call is iterated in fix commits.
#
# Reference upstream:
#   from vllm_omni.entrypoints import AsyncOmni
#   from vllm_omni.outputs import OmniRequestOutput

# Skeleton intentionally exports nothing yet so the image builds and
# the engine-registration path resolves. Iteration commits in this
# same branch will populate ``builder()`` mirroring the v0.17.1 vLLM
# app.py.


def builder(args: dict):  # pragma: no cover - skeleton
    """Build the Ray Serve application.

    Args:
        args: engine arguments forwarded by Neutree Ray orchestrator.

    Raises:
        NotImplementedError: skeleton only — populated during build-test
            loop iteration.
    """
    raise NotImplementedError(
        "vllm-omni v0.18.0 Ray Serve app.py is a Phase 1 skeleton; "
        "the AsyncOmni integration is filled in via the build-test "
        "loop. See cluster-image-builder/serve/vllm-omni/v0_18_0/app.py "
        "module docstring for the OMNI-TODO checklist."
    )
