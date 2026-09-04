#!/usr/bin/env python3
"""Call ONR through the OpenAI Python SDK Chat Completions API."""

import argparse
import os
import sys

try:
    from openai import OpenAI
except ImportError:
    OpenAI = None


def parse_args():
    parser = argparse.ArgumentParser(
        description="Send an OpenAI Chat Completions request through ONR."
    )
    parser.add_argument(
        "--base-url",
        default=os.getenv("ONR_BASE_URL", "http://127.0.0.1:3300"),
        help="ONR base URL",
    )
    parser.add_argument(
        "--access-key",
        default=os.getenv("ONR_ACCESS_KEY", ""),
        help="ONR Access Key; defaults to ONR_ACCESS_KEY",
    )
    parser.add_argument(
        "--model",
        default=os.getenv("ONR_MODEL", "qwen3.8-max"),
        help="Public model ID",
    )
    parser.add_argument(
        "--provider",
        default=os.getenv("ONR_PROVIDER", "ctyun"),
        help="Provider selector sent in x-onr-provider",
    )
    parser.add_argument(
        "--prompt",
        default=os.getenv("ONR_PROMPT", "你好，请用一句话介绍你自己。"),
        help="User prompt",
    )
    parser.add_argument(
        "--stream",
        action="store_true",
        default=os.getenv("ONR_STREAM", "false").lower() == "true",
        help="Request and print an SSE streaming response",
    )
    return parser.parse_args()


def main():
    args = parse_args()
    if not args.access_key:
        print(
            "error: Access Key is required; set ONR_ACCESS_KEY or use --access-key",
            file=sys.stderr,
        )
        return 2
    if not args.model:
        print("error: model is required", file=sys.stderr)
        return 2

    if OpenAI is None:
        print("error: the openai package is required; run: python3 -m pip install openai", file=sys.stderr)
        return 1

    base_url = args.base_url.rstrip("/") + "/v1"
    client_options = {"api_key": args.access_key, "base_url": base_url, "timeout": 120.0}
    client = OpenAI(**client_options)

    print(f"POST {base_url}/chat/completions")
    print(f"model={args.model} stream={str(args.stream).lower()}")

    try:
        completion = client.chat.completions.create(
            model=args.model,
            messages=[{"role": "user", "content": args.prompt}],
            max_tokens=128,
            stream=args.stream,
        )
        if args.stream:
            for chunk in completion:
                content = chunk.choices[0].delta.content if chunk.choices else None
                if content:
                    sys.stdout.write(content)
                    sys.stdout.flush()
            sys.stdout.write("\n")
        else:
            print(completion.model_dump_json(indent=2))
        return 0
    except Exception as error:  # The SDK exposes multiple transport/API error classes.
        print(f"error: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
