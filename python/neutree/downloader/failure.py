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

* **It is user-visible, so the container's own credentials are redacted.**
  `sanitize()` removes the *literal values* of the environment variables a token
  can arrive in (`utils.CREDENTIAL_ENV_VARS`). That is an exact match, not a
  guess at what a secret looks like, so it holds however the value was rendered
  — quoted in a dict, in an `Authorization` header, or bare in prose.

  There is deliberately no pattern-matching backstop here. An earlier version
  carried regexes for `Authorization` headers, signed-URL query parameters and
  `hf_`/`ms-`/`sk-` token shapes; they were removed because they were the wrong
  instrument twice over. For the case that matters — this container's own token
  — they only duplicated the exact match. For the case they nominally covered —
  text written by the hub or by `huggingface_hub` — they scanned for
  credential-shaped fragments, while what is actually risky about foreign text
  is the content itself. That is handled structurally instead: whitespace is
  collapsed (no injected log lines), the message is capped, and a URL is reduced
  to `scheme://netloc` at the point it is interpolated, so a signed CDN URL's
  query string never enters a message in the first place.

  Add a pattern here when a real leak path is demonstrated, not in anticipation
  of one: an unfalsifiable blocklist reads as protection and grants none.
* **It is truncated by kubelet at 4096 bytes.** A message longer than that would
  be cut wherever the limit lands, which is exactly where the actionable
  sentence tends to be. The head is kept, on purpose: these messages lead with
  the reason and trail into context.
"""

import os
from typing import Optional

from .utils import CREDENTIAL_ENV_VARS

# kubelet caps a termination message at 4096 bytes and truncates the tail. Stay
# under it with room for the multi-byte characters a model id or path can carry.
MAX_MESSAGE_BYTES = 3500

TERMINATION_LOG_ENV = "NEUTREE_TERMINATION_LOG"
DEFAULT_TERMINATION_LOG = "/dev/termination-log"

_REDACTED = "<redacted>"

# Below this length a credential is not distinguishable from ordinary words, and
# replacing it would mangle the message rather than protect anything.
_MIN_REDACTABLE_LENGTH = 4


def sanitize(text: str) -> str:
    """Remove this container's credentials from a message headed for a user.

    Exact-value replacement over `CREDENTIAL_ENV_VARS`, which is the whole of it
    — see the module docstring for why there is no pattern-matching backstop.
    Very short values are left alone: a two-character token is indistinguishable
    from ordinary text, and blanking it would corrupt the message it appears in.
    """
    if not text:
        return ""

    cleaned = str(text)

    for name in CREDENTIAL_ENV_VARS:
        value = os.environ.get(name)
        if value and len(value) >= _MIN_REDACTABLE_LENGTH:
            cleaned = cleaned.replace(value, _REDACTED)

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
