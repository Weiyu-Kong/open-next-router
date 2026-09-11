#!/usr/bin/env python3
"""Add credit to one ONR access key through the admin API."""

from __future__ import annotations

import argparse
import getpass
import json
import sys
import urllib.error
import urllib.request
from decimal import Decimal, InvalidOperation
from pathlib import Path
from typing import Any


def load_object(path: Path) -> dict[str, Any]:
    with path.open(encoding="utf-8") as handle:
        value = json.load(handle)
    if not isinstance(value, dict):
        raise ValueError(f"{path} must contain a JSON object")
    return value


def normalize_url(raw: str) -> str:
    raw = raw.strip().rstrip("/")
    if not raw:
        raise ValueError("the admin API URL is empty")
    if "://" not in raw:
        raw = "https://" + raw
    return raw


def request_json(
    base_url: str,
    method: str,
    path: str,
    payload: dict[str, Any] | None,
    timeout: float,
    admin_token: str = "",
) -> dict[str, Any]:
    body = None if payload is None else json.dumps(payload).encode()
    headers = {"Accept": "application/json", "Content-Type": "application/json"}
    if admin_token:
        headers["Authorization"] = f"Bearer {admin_token}"
    request = urllib.request.Request(base_url + path, data=body, method=method, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            result = json.load(response)
    except urllib.error.HTTPError as exc:
        detail = exc.read(4096).decode(errors="replace")
        raise RuntimeError(f"{method} {path} returned HTTP {exc.code}: {detail}") from exc
    except urllib.error.URLError as exc:
        raise RuntimeError(f"{method} {path} failed: {exc.reason}") from exc
    if not isinstance(result, dict):
        raise RuntimeError(f"{method} {path} returned a non-object response")
    return result


def resolve_key_name(base_url: str, access_key: str, timeout: float) -> str:
    response = request_json(
        base_url,
        "POST",
        "/api/user/login",
        {"access_key": access_key},
        timeout,
    )
    account = response.get("account") or {}
    name = str(account.get("access_key_id", "")).strip()
    if not response.get("ok") or not name:
        raise RuntimeError("could not resolve the supplied access key")
    return name


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=Path("config.json"))
    parser.add_argument(
        "--access-key",
        help="Target access key; omit this option to enter it securely at the prompt",
    )
    parser.add_argument("--amount", required=True, help="Positive credit amount, for example 200")
    parser.add_argument(
        "--operation-id",
        required=True,
        help="Unique identifier for this credit operation; reuse it when retrying",
    )
    parser.add_argument("--currency", default="CNY")
    parser.add_argument("--api-base-url", help="Override the API URL from config.json")
    parser.add_argument("--timeout", type=float, default=30.0)
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    try:
        amount = Decimal(args.amount)
    except InvalidOperation as exc:
        raise ValueError("--amount must be a decimal number") from exc
    if not amount.is_finite() or amount <= 0:
        raise ValueError("--amount must be greater than zero")
    operation_id = args.operation_id.strip()
    if not operation_id or any(character.isspace() for character in operation_id):
        raise ValueError("--operation-id must be non-empty and contain no whitespace")

    config = load_object(args.config)
    base_url = normalize_url(str(args.api_base_url or config.get("api", "")))
    admin_token = str(config.get("admin_token", "")).strip()
    if not admin_token:
        raise ValueError("config.json requires a non-empty admin_token")
    access_key = str(args.access_key or "").strip()
    if not access_key and not args.dry_run:
        access_key = getpass.getpass("Target Access Key: ").strip()
    if not access_key and args.dry_run:
        raise ValueError("--access-key is required with --dry-run")
    if not access_key:
        raise ValueError("the target access key is empty")
    print(f"Validated one target access key; amount={amount} {args.currency.upper()}.")
    if args.dry_run:
        print("Dry run completed; no API requests were sent.")
        return 0

    name = resolve_key_name(base_url, access_key, args.timeout)
    print("Resolved the target access-key account.")
    response = request_json(
        base_url,
        "POST",
        "/api/admin/access-keys/batch",
        {
            "action": "credit",
            "names": [name],
            "amount": format(amount, "f"),
            "currency": args.currency.upper(),
            "idempotency_prefix": f"manual-credit:{operation_id}",
        },
        args.timeout,
        admin_token,
    )
    results = response.get("results") or []
    failed = [result for result in results if not isinstance(result, dict) or not result.get("ok")]
    if not response.get("ok") or len(results) != 1 or failed:
        details = "; ".join(str(item.get("error", "unknown error")) for item in failed[:5] if isinstance(item, dict))
        raise RuntimeError(
            f"credit operation incomplete: expected=1 returned={len(results)} "
            f"failed={len(failed)}{': ' + details if details else ''}"
        )
    print("Credited the target access-key account successfully.")
    print(f"Operation ID: {operation_id} (safe to reuse only when retrying this same operation).")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ValueError, RuntimeError, OSError, json.JSONDecodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        raise SystemExit(1)
