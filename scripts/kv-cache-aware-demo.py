#!/usr/bin/env python3
"""Measure TTFT and E2E for a deterministic shared-prefix workload."""

import argparse
import json
import math
import os
import statistics
import time
import urllib.error
import urllib.request


def _request(url, api_key, body, timeout_s):
    headers = {"Content-Type": "application/json"}
    if api_key:
        headers["Authorization"] = f"Bearer {api_key}"
    request = urllib.request.Request(
        url,
        data=json.dumps(body).encode("utf-8"),
        headers=headers,
        method="POST",
    )
    started = time.perf_counter()
    first_data_at = None
    first_token_at = None
    try:
        with urllib.request.urlopen(request, timeout=timeout_s) as response:
            for raw_line in response:
                line = raw_line.decode("utf-8", errors="replace").strip()
                if not line.startswith("data:"):
                    continue
                payload = line.removeprefix("data:").strip()
                if payload == "[DONE]":
                    break
                if first_data_at is None:
                    first_data_at = time.perf_counter()
                try:
                    chunk = json.loads(payload)
                except json.JSONDecodeError:
                    continue
                if isinstance(chunk, dict) and "error" in chunk:
                    raise RuntimeError(f"Streaming error: {chunk['error']}")
                choices = chunk.get("choices") if isinstance(chunk, dict) else None
                if not isinstance(choices, list):
                    continue
                if any(
                    isinstance(choice, dict)
                    and isinstance(choice.get("delta"), dict)
                    and choice["delta"].get("content")
                    for choice in choices
                ):
                    first_token_at = time.perf_counter()
                    break

            for _ in response:
                pass
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"HTTP {exc.code}: {detail}") from exc
    finished = time.perf_counter()
    first = first_token_at or first_data_at
    if first is None:
        raise RuntimeError("Response stream contained no data chunks")
    return first - started, finished - started


def _percentile(values, percentile):
    ordered = sorted(values)
    index = max(0, math.ceil(len(ordered) * percentile) - 1)
    return ordered[index]


def main():
    parser = argparse.ArgumentParser(
        description="Warm and replay a long shared prefix against a Neutree endpoint"
    )
    parser.add_argument("--label", default="run")
    parser.add_argument(
        "--base-url",
        required=True,
        help="Endpoint base URL, including /<workspace>/<endpoint> when applicable",
    )
    parser.add_argument("--model", required=True, help="Served model name")
    parser.add_argument("--requests", type=int, default=8)
    parser.add_argument("--prefix-repetitions", type=int, default=512)
    parser.add_argument(
        "--churn-requests",
        type=int,
        default=0,
        help="Unique long-prefix requests sent after warmup to evict GPU KV",
    )
    parser.add_argument("--stats-wait-seconds", type=float, default=2.0)
    parser.add_argument("--timeout-seconds", type=float, default=180.0)
    parser.add_argument(
        "--api-key",
        default=os.getenv("NEUTREE_API_KEY", ""),
        help="Bearer token; defaults to NEUTREE_API_KEY",
    )
    args = parser.parse_args()
    if (
        args.requests <= 0
        or args.prefix_repetitions <= 0
        or args.churn_requests < 0
    ):
        parser.error(
            "--requests/--prefix-repetitions must be positive and "
            "--churn-requests must be non-negative"
        )

    url = f"{args.base_url.rstrip('/')}/v1/chat/completions"
    shared_prefix = (
        "Neutree KV cache routing demo shared context. "
        * args.prefix_repetitions
    )

    def body(suffix, prefix=shared_prefix):
        return {
            "model": args.model,
            "messages": [
                {"role": "system", "content": prefix},
                {"role": "user", "content": suffix},
            ],
            "temperature": 0,
            "max_tokens": 8,
            "stream": True,
        }

    warmup_ttft, warmup_e2e = _request(
        url,
        args.api_key,
        body("warm this shared prefix"),
        args.timeout_seconds,
    )
    print(
        f"label={args.label} warmup_ttft_seconds={warmup_ttft:.3f} "
        f"warmup_e2e_seconds={warmup_e2e:.3f}"
    )
    time.sleep(max(0.0, args.stats_wait_seconds))

    for index in range(args.churn_requests):
        churn_prefix = (
            f"Neutree unique GPU cache churn context {index}. "
            * args.prefix_repetitions
        )
        churn_ttft, churn_e2e = _request(
            url,
            args.api_key,
            body(f"churn request {index}", churn_prefix),
            args.timeout_seconds,
        )
        print(
            f"label={args.label} churn={index + 1} "
            f"ttft_seconds={churn_ttft:.3f} e2e_seconds={churn_e2e:.3f}"
        )
    if args.churn_requests:
        time.sleep(max(0.0, args.stats_wait_seconds))

    ttfts = []
    e2es = []
    for index in range(args.requests):
        ttft, e2e = _request(
            url,
            args.api_key,
            body(f"replay request {index}"),
            args.timeout_seconds,
        )
        ttfts.append(ttft)
        e2es.append(e2e)
        print(
            f"label={args.label} request={index + 1} "
            f"ttft_seconds={ttft:.3f} e2e_seconds={e2e:.3f}"
        )

    print(
        "summary "
        f"label={args.label} requests={len(ttfts)} "
        f"median_ttft_seconds={statistics.median(ttfts):.3f} "
        f"p95_ttft_seconds={_percentile(ttfts, 0.95):.3f} "
        f"median_e2e_seconds={statistics.median(e2es):.3f} "
        f"p95_e2e_seconds={_percentile(e2es, 0.95):.3f}"
    )


if __name__ == "__main__":
    main()
