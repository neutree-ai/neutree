"""Carry a download failure's reason out of the init container.

The downloader already knows *why* it failed — `ModelScopeDownloadUnavailable`
says in words that runtime download is unavailable and that the cache holds no
copy — but that knowledge died with the process. Kubernetes reports a failed
init container to the orchestrator as an exit code, a restart count and
`state.terminated.message`, and that message is only ever populated from the
container's termination-message file. Nothing wrote it, so the endpoint's
`status.error_message` ended at the colon:

    Init Container 'model-downloader' terminated with exit code 1 after 5 restarts:

Writing the reason to `/dev/termination-log` is what puts the text after that
colon, through kubelet, into the endpoint status, and onto the user's screen —
without them reading container logs, which in an air-gapped cluster is exactly
the step they cannot easily take.

Two constraints shape the text that goes there:

* **It is user-visible, so it is sanitized.** A hub error can quote the URL that
  produced it, and a signed CDN URL carries its credential in the query string;
  a wrapped exception can carry a header dict. `sanitize()` redacts those, and
  additionally redacts the *literal values* of the token environment variables
  this container was given — the one check that does not depend on guessing the
  shape of a secret.
* **It is truncated by kubelet at 4096 bytes.** A message longer than that would
  be cut wherever the limit lands, which is exactly where the actionable
  sentence tends to be. The head is kept, on purpose: these messages lead with
  the reason and trail into context.
"""

import os
import re
from typing import Optional

# kubelet caps a termination message at 4096 bytes and truncates the tail. Stay
# under it with room for the multi-byte characters a model id or path can carry.
MAX_MESSAGE_BYTES = 3500

TERMINATION_LOG_ENV = "NEUTREE_TERMINATION_LOG"
DEFAULT_TERMINATION_LOG = "/dev/termination-log"

# Environment variables whose value is a credential. Their literal values are
# redacted out of any message, which catches a leak no pattern below would.
_SECRET_ENV_VARS = (
    "NEUTREE_DL_TOKEN",
    "HF_TOKEN",
    "HUGGING_FACE_HUB_TOKEN",
    "MODELSCOPE_API_TOKEN",
)

_REDACTED = "<redacted>"

_PATTERNS = (
    # Authorization header, however it was rendered (str(), repr(), a dict).
    (re.compile(r"(?i)(authorization\s*[:=]\s*['\"]?\s*(?:bearer|basic|token)?\s*)\S+"),
     r"\1" + _REDACTED),
    (re.compile(r"(?i)\bbearer\s+[A-Za-z0-9._\-~+/]+=*"), "Bearer " + _REDACTED),
    # Credential-bearing query parameters, including the signed-URL family a
    # ModelScope/CDN redirect produces.
    (re.compile(r"(?i)\b((?:access[_-]?token|api[_-]?key|apikey|auth|credential|password|"
                r"secret|signature|sig|token|x-amz-[a-z-]*(?:signature|credential|security-token))"
                r")=[^&\s'\"]+"),
     r"\1=" + _REDACTED),
    # Mapping/keyword renderings: {'token': 'ms-...'}, token='ms-...'.
    (re.compile(r"(?i)(['\"]?(?:access[_-]?token|api[_-]?key|apikey|credential|password|secret|token)"
                r"['\"]?\s*[:=]\s*)['\"][^'\"]+['\"]"),
     r"\1'" + _REDACTED + "'"),
    # Well-known token shapes, in case one is quoted bare.
    (re.compile(r"\bhf_[A-Za-z0-9]{8,}"), _REDACTED),
    (re.compile(r"\bms-[0-9a-fA-F]{8,}[0-9a-fA-F-]*"), _REDACTED),
    (re.compile(r"\bsk-[A-Za-z0-9]{8,}"), _REDACTED),
)


def sanitize(text: str) -> str:
    """Strip credentials out of a message that is about to be shown to a user."""
    if not text:
        return ""

    cleaned = str(text)

    # Literal secret values first: a token that also matches a pattern below is
    # redacted either way, but one that matches none is only caught here.
    for name in _SECRET_ENV_VARS:
        value = os.environ.get(name)
        if value and len(value) >= 4:
            cleaned = cleaned.replace(value, _REDACTED)

    for pattern, replacement in _PATTERNS:
        cleaned = pattern.sub(replacement, cleaned)

    return cleaned


def _truncate(text: str) -> str:
    encoded = text.encode("utf-8")
    if len(encoded) <= MAX_MESSAGE_BYTES:
        return text

    # Cut on a character boundary, not a byte one, or the message stops being
    # decodable text.
    return encoded[:MAX_MESSAGE_BYTES].decode("utf-8", "ignore").rstrip() + " ...(truncated)"


def describe_exception(exc: BaseException) -> str:
    """Render an exception chain as one line of actionable prose.

    The chain matters: the downloader wraps a socket error into
    `ModelScopeDownloadUnavailable`, and `str()` of the outer exception alone is
    usually the actionable half while the inner one names what actually broke.
    Causes are appended only when they add text the outer message does not
    already quote — urllib errors get interpolated into their wrapper, and
    repeating them reads as two separate faults.
    """
    parts = []
    seen = set()
    current: Optional[BaseException] = exc
    depth = 0

    while current is not None and depth < 4:
        text = str(current).strip() or type(current).__name__
        if text not in seen and not any(text in part for part in parts):
            parts.append(text)
            seen.add(text)

        current = current.__cause__ or current.__context__
        depth += 1

    joined = ": caused by ".join(parts)

    return " ".join(joined.split())


def build_failure_message(exc: BaseException, *, model_name: str = "",
                          registry_type: str = "") -> str:
    """The one line an operator should be able to act on, ready for the status."""
    subject = "model download failed"
    if model_name:
        qualifier = f"{registry_type} model '{model_name}'" if registry_type else f"model '{model_name}'"
        subject = f"model download failed for {qualifier}"

    return _truncate(sanitize(f"{subject}: {describe_exception(exc)}"))


def write_termination_message(message: str) -> None:
    """Best-effort write of the failure reason to the termination-message file.

    Best-effort on purpose: this runs on a path that is already failing, and a
    container whose termination-message file is unwritable must still exit with
    its original status rather than dying on the reporting of it. The message is
    printed by the caller regardless, so nothing is lost from the logs.
    """
    path = os.environ.get(TERMINATION_LOG_ENV) or DEFAULT_TERMINATION_LOG

    try:
        with open(path, "w", encoding="utf-8") as handle:
            handle.write(_truncate(message))
    except OSError:
        pass
