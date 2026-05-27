import logging
import os

from .runtime import PDRouterSidecar, SidecarConfig, UrllibEngineClient, run_server


def main() -> None:
    logging.basicConfig(
        level=os.environ.get("NEUTREE_PD_LOG_LEVEL", "INFO"),
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )
    config = SidecarConfig.from_env()
    client = UrllibEngineClient(
        timeout=float(os.environ.get("NEUTREE_PD_ENGINE_TIMEOUT_SECONDS", "600")),
        health_timeout=float(os.environ.get("NEUTREE_PD_HEALTH_TIMEOUT_SECONDS", "0.5")),
    )
    run_server(
        PDRouterSidecar(config, client),
        host=os.environ.get("NEUTREE_PD_SIDECAR_HOST", "0.0.0.0"),
        port=config.sidecar_port,
    )


if __name__ == "__main__":
    main()
