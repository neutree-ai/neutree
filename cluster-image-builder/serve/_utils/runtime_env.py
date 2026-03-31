"""Helpers for building Ray Serve per-deployment runtime_env dicts."""

from typing import Any, Dict


def build_backend_runtime_env(backend_container: Dict[str, Any]) -> Dict[str, Any]:
    """Build a runtime_env for the Backend deployment with container config.

    Ray replaces runtime_env per-deployment (no merge with the app-level
    runtime_env).  This function creates a standalone runtime_env that
    includes the given *backend_container* config **and** carries forward
    any ``env_vars`` defined in the app-level runtime_env so that
    environment variables (HF token, engine identity, etc.) are available
    inside the Backend container.
    """
    import ray

    runtime_env: Dict[str, Any] = {"container": backend_container}
    try:
        app_env_vars = ray.get_runtime_context().runtime_env.get("env_vars")
        if app_env_vars:
            runtime_env["env_vars"] = app_env_vars
    except (AttributeError, KeyError, TypeError):
        pass
    return runtime_env
