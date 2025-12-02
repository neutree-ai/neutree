import os
import shutil
from typing import Callable
from typing import Dict, Any, Tuple
from .entity import DownloadRequest


def ensure_dir(path: str) -> None:
    os.makedirs(path, exist_ok=True)

def copy_tree(src: str, dest: str, *, on_progress: Callable[[str], None] | None = None) -> None:
    """Recursively copy src to dest. Calls on_progress for each file copied."""
    ensure_dir(dest)
    if os.path.isfile(src):
        shutil.copy2(src, dest)
        if on_progress:
            on_progress(src)
        return

    for root, dirs, files in os.walk(src):
        rel = os.path.relpath(root, src)
        target_root = os.path.join(dest, rel) if rel != os.curdir else dest
        ensure_dir(target_root)
        for f in files:
            s = os.path.join(root, f)
            t = os.path.join(target_root, f)
            shutil.copy2(s, t)
            if on_progress:
                on_progress(s)

def env_bool(key: str, default: bool) -> bool:
    v = os.environ.get(key)
    if v is None:
        return default
    return v.lower() not in ("0", "false", "no")

def build_request_from_model_args(model_args: Dict[str, Any]) -> Tuple[str, DownloadRequest]:
    """Convert high-level model_args + environment into (backend, DownloadRequest).

    Rules (same as previous implementation):
    - backend: NEUTREE_DL_BACKEND env > model_args.registry-type heuristics > resource scheme heuristics > 'nfs'
    - source: model_args.path or model_args.name
    - dest: NEUTREE_DL_DEST or NEUTREE_DL_CACHE_DIR or '/models'
    - credentials: model_args.credentials (dict) or token from NEUTREE_DL_TOKEN/NEUTREE_HF_TOKEN
    - recursive/overwrite/retries/timeout read from env or defaults
    """
    backend = os.environ.get("NEUTREE_DL_BACKEND")
    if not backend:
        rt = (model_args.get("registry_type") or "")
        if isinstance(rt, str) and "hugging-face" in rt.lower():
            backend = "hugging-face"
    if not backend:
        # infer from path/name heuristics
        candidate = model_args.get("registry_path") or model_args.get("name") or ""
        if isinstance(candidate, str):
            if candidate.startswith("/"):
                backend = "local"
            elif candidate.startswith("http://") or candidate.startswith("https://"):
                backend = "http"

    if not backend:
        backend = "local"

    source = model_args.get("registry_path") or model_args.get("name") or ""

    cache_dir = os.environ.get("NEUTREE_MODEL_CACHE_DIR") or "/models-cache"
    dest = cache_dir + "/" + model_args.get("registry_type", "hugging-face")+ "/" + model_args.get("name", "default")
    if backend != "hugging-face":
        if model_args.get("version") is not None:
            dest = dest + "/" + str(model_args.get("version"))

    credentials = None
    token = os.environ.get("NEUTREE_DL_TOKEN") or os.environ.get("HF_TOKEN")
    if token and token != "":
        credentials = {"token": token}

    recursive = env_bool("NEUTREE_DL_RECURSIVE", True)
    overwrite = env_bool("NEUTREE_DL_OVERWRITE", False)
    retries = int(os.environ.get("NEUTREE_DL_RETRIES", "3"))
    timeout = None
    if os.environ.get("NEUTREE_DL_TIMEOUT"):
        try:
            timeout = float(os.environ.get("NEUTREE_DL_TIMEOUT"))
        except Exception:
            timeout = None

    dl_req = DownloadRequest(source=source, dest=dest, credentials=credentials,
                             recursive=recursive, overwrite=overwrite, retries=retries,
                             timeout=timeout, metadata=model_args)
    return backend, dl_req