#!/usr/bin/env python3
"""Run a concurrent OpenAI-compatible chat completion load test."""

import argparse
import concurrent.futures
import json
import os
import threading
import time
import urllib.error
import urllib.request
from collections import Counter


def percentile(values, fraction):
    if not values:
        return None
    ordered = sorted(values)
    index = min(len(ordered) - 1, int((len(ordered) - 1) * fraction))
    return ordered[index]


def default_chat_url():
    base_url = os.getenv("ONR_BASE_URL", "http://127.0.0.1:3300").rstrip("/")
    if base_url.endswith("/v1"):
        return base_url + "/chat/completions"
    return base_url + "/v1/chat/completions"


def parse_args():
    parser = argparse.ArgumentParser(
        description="Send concurrent non-streaming chat completion requests to ONR."
    )
    parser.add_argument(
        "--url",
        default=default_chat_url(),
        help="Chat completions URL (default: ONR_BASE_URL + /v1/chat/completions).",
    )
    parser.add_argument(
        "--api-key",
        default=os.getenv("ONR_ACCESS_KEY", os.getenv("ONR_ACCESS_KEY_DEFAULT", "")),
        help="ONR Access Key; prefer ONR_ACCESS_KEY to avoid shell history exposure.",
    )
    parser.add_argument("--model", default="qwen3.8-max")
    parser.add_argument("--prompt", default="Reply with exactly: load-test-ok")
    parser.add_argument("-n", "--requests", type=int, default=200)
    parser.add_argument("-c", "--concurrency", type=int, default=200)
    parser.add_argument("--max-tokens", type=int, default=16)
    parser.add_argument("--timeout", type=float, default=120.0)
    parser.add_argument(
        "--verbose-errors",
        action="store_true",
        help="Print one line for each failed request.",
    )
    args = parser.parse_args()
    if args.requests <= 0 or args.concurrency <= 0:
        parser.error("--requests and --concurrency must be positive")
    if args.max_tokens <= 0 or args.timeout <= 0:
        parser.error("--max-tokens and --timeout must be positive")
    args.concurrency = min(args.concurrency, args.requests)
    if not args.api_key:
        parser.error("missing Access Key; set ONR_ACCESS_KEY or pass --api-key")
    return args


def run_one(index, args, start_barrier):
    if index < args.concurrency:
        start_barrier.wait(timeout=30)
    started = time.perf_counter()
    request_body = json.dumps(
        {
            "model": args.model,
            "messages": [{"role": "user", "content": args.prompt}],
            "max_tokens": args.max_tokens,
            "stream": False,
        },
        separators=(",", ":"),
    ).encode("utf-8")
    request = urllib.request.Request(
        args.url,
        data=request_body,
        headers={
            "Authorization": "Bearer " + args.api_key,
            "Content-Type": "application/json",
            "Accept": "application/json",
            "Connection": "keep-alive",
        },
        method="POST",
    )
    first_byte_ms = None
    try:
        with urllib.request.urlopen(request, timeout=args.timeout) as response:
            first_byte = response.read(1)
            first_byte_ms = (time.perf_counter() - started) * 1000
            response.read()
            finished = time.perf_counter()
            return {
                "index": index,
                "status": response.status,
                "first_byte_ms": first_byte_ms if first_byte else None,
                "total_ms": (finished - started) * 1000,
                "error": "",
            }
    except urllib.error.HTTPError as error:
        finished = time.perf_counter()
        return {
            "index": index,
            "status": error.code,
            "first_byte_ms": first_byte_ms,
            "total_ms": (finished - started) * 1000,
            "error": "HTTP %s" % error.code,
        }
    except Exception as error:  # noqa: BLE001 - report network failures per request.
        finished = time.perf_counter()
        return {
            "index": index,
            "status": "error",
            "first_byte_ms": first_byte_ms,
            "total_ms": (finished - started) * 1000,
            "error": "%s: %s" % (type(error).__name__, error),
        }
def main():
    args = parse_args()
    start_barrier = threading.Barrier(args.concurrency + 1)
    results = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency) as pool:
        futures = [pool.submit(run_one, index, args, start_barrier) for index in range(args.requests)]
        start_barrier.wait(timeout=30)
        test_started = time.perf_counter()
        for future in concurrent.futures.as_completed(futures):
            results.append(future.result())
    elapsed_ms = (time.perf_counter() - test_started) * 1000

    statuses = Counter(str(result["status"]) for result in results)
    successful = [result for result in results if isinstance(result["status"], int) and 200 <= result["status"] < 300]
    all_latencies = [result["total_ms"] for result in results]
    successful_latencies = [result["total_ms"] for result in successful]
    first_bytes = [result["first_byte_ms"] for result in results if result["first_byte_ms"] is not None]

    print("ONR concurrent load test")
    print("url=%s model=%s requests=%d concurrency=%d" % (args.url, args.model, args.requests, args.concurrency))
    print("elapsed_ms=%.1f throughput_rps=%.2f" % (elapsed_ms, len(results) / (elapsed_ms / 1000)))
    print("success=%d/%d success_rate=%.2f%%" % (len(successful), len(results), len(successful) * 100 / len(results)))
    print("statuses=%s" % ", ".join("%s:%d" % item for item in sorted(statuses.items())))
    print("latency_ms_all=%s" % format_percentiles(all_latencies))
    print("latency_ms_success=%s" % format_percentiles(successful_latencies))
    print("first_byte_ms=%s" % format_percentiles(first_bytes))

    if args.verbose_errors:
        for result in sorted(results, key=lambda item: item["index"]):
            if result["error"]:
                print("request=%d status=%s error=%s" % (result["index"], result["status"], result["error"]))
    return 0 if len(successful) == len(results) else 1


def format_percentiles(values):
    if not values:
        return "n/a"
    return " ".join(
        "%s=%.1f" % (name, value)
        for name, value in (
            ("avg", sum(values) / len(values)),
            ("min", percentile(values, 0)),
            ("p50", percentile(values, 0.50)),
            ("p90", percentile(values, 0.90)),
            ("p95", percentile(values, 0.95)),
            ("p99", percentile(values, 0.99)),
            ("max", percentile(values, 1)),
        )
    )


if __name__ == "__main__":
    raise SystemExit(main())
