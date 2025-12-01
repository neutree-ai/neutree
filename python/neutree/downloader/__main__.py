"""CLI entrypoint for the downloader package.
"""
import argparse
import sys

from .utils import build_request_from_model_args


def _build_parser():
    p = argparse.ArgumentParser(prog="neutree.downloader")
    # Only expose model-related args in CLI. Other behavior/configuration should be
    # controlled via environment variables (NEUTREE_DL_*) so orchestrator does not
    # need to pass runtime flags.
    # model parameters passed from orchestrator
    p.add_argument("--name", required=True, help="model name")
    p.add_argument("--version", required=False, help="model version")
    p.add_argument("--file", required=False, help="specific file to download inside model path")
    p.add_argument("--task", required=False, help="model task (informational)")
    p.add_argument("--registry_path", required=False, help="explicit registry path for the model")
    p.add_argument("--registry_type", required=True, help="registry type (e.g., hugging-face, bentoml)")

    return p


def main(argv=None):
    argv = argv if argv is not None else sys.argv[1:]
    parser = _build_parser()
    args = parser.parse_args(argv)

    model_args = {
        "name": args.name,
        "version": args.version,
        "file": args.file,
        "task": args.task,
        "registry_path": args.registry_path,
        "registry_type": args.registry_type,
    }
    # Build low-level DownloadRequest from model_args + environment using utils helper
    backend, dl_req = build_request_from_model_args(model_args)

    # main now produces (backend, low-level DownloadRequest), obtain backend impl and run
    from .dispatcher import get_downloader

    print(f"Performing download backend={backend} source={dl_req.source} dest={dl_req.dest}")
    downloader = get_downloader(backend)
    downloader.download(dl_req.source, dl_req.dest, credentials=dl_req.credentials,
                        recursive=dl_req.recursive, overwrite=dl_req.overwrite,
                        retries=dl_req.retries, timeout=dl_req.timeout, metadata=dl_req.metadata)
    print("Download finished")


if __name__ == "__main__":
    main()
