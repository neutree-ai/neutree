"""Root conftest that stubs huggingface_hub when the real package is absent.

This runs before pytest imports any test modules, so the package __init__.py
can resolve ``from huggingface_hub.utils.sha import ...`` without error.
"""

import hashlib
import sys
import types

_fake_sha = types.ModuleType("huggingface_hub.utils.sha")
_fake_sha.git_hash = lambda data: ""
_fake_sha.sha_fileobj = lambda stream, bufsize=0: hashlib.sha256(stream.read()).digest()

_fake_hf_api = types.ModuleType("huggingface_hub.hf_api")
_fake_hf_api.RepoFile = type("RepoFile", (), {})

for name, mod in {
    "huggingface_hub": types.ModuleType("huggingface_hub"),
    "huggingface_hub.utils": types.ModuleType("huggingface_hub.utils"),
    "huggingface_hub.utils.sha": _fake_sha,
    "huggingface_hub.hf_api": _fake_hf_api,
}.items():
    sys.modules.setdefault(name, mod)
