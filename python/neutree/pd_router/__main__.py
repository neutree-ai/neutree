import logging
import os

from .runtime import PDRouter, RouterConfig, UrllibEngineClient, run_server


def main() -> None:
    logging.basicConfig(
        level=os.environ.get("NEUTREE_PD_LOG_LEVEL", "INFO"),
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )
    config = RouterConfig.from_env()
    client = UrllibEngineClient(
        timeout=float(os.environ.get("NEUTREE_PD_ENGINE_TIMEOUT_SECONDS", "600")),
        health_timeout=float(os.environ.get("NEUTREE_PD_HEALTH_TIMEOUT_SECONDS", "0.5")),
    )
    run_server(
        PDRouter(config, client),
        host=os.environ.get("NEUTREE_PD_ROUTER_HOST", "0.0.0.0"),
        port=config.router_port,
    )


if __name__ == "__main__":
    main()
