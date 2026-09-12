#!/usr/bin/env python3
"""Disable one ONR access key through the admin API."""

from __future__ import annotations

import argparse
import getpass
import json
import sys
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any


def load_config(path: Path) -> dict[str, Any]:
    with path.open(encoding="utf-8") as handle:
        value = json.load(handle)
    if not isinstance(value, dict):
        raise ValueError(f"{path} must contain a JSON object")
    return value


def normalize_url(value: str) -> str:
    value = value.strip().rstrip("/")
    if not value:
        raise ValueError("the admin API URL is empty")
    if "://" not in value:
        value = "https://" + value
    return value


def request_json(
    base_url: str,
    method: str,
    path: str,
    payload: dict[str, Any] | None,
    timeout: float,
    admin_token: str = "",
) -> tuple[int, dict[str, Any]]:
    body = None if payload is None else json.dumps(payload).encode()
    headers = {"Accept": "application/json", "Content-Type": "application/json"}
    if admin_token:
        headers["Authorization"] = f"Bearer {admin_token}"
    request = urllib.request.Request(base_url + path, data=body, method=method, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            value = json.load(response)
            return response.status, value if isinstance(value, dict) else {}
    except urllib.error.HTTPError as exc:
        try:
            value = json.loads(exc.read(4096))
        except (json.JSONDecodeError, UnicodeDecodeError):
            value = {}
        return exc.code, value if isinstance(value, dict) else {}
    except urllib.error.URLError as exc:
        raise RuntimeError(f"{method} {path} failed: {exc.reason}") from exc


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=Path("config.json"))
    parser.add_argument("--access-key", help="Target key; omit to enter it securely at the prompt")
    parser.add_argument("--api-base-url", help="Override the API URL from config.json")
    parser.add_argument("--timeout", type=float, default=30.0)
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    config = load_config(args.config)
    base_url = normalize_url(str(args.api_base_url or config.get("api") or ""))
    admin_token = str(config.get("admin_token") or "").strip()
    if not admin_token:
        raise ValueError("config.json requires a non-empty admin_token")
    access_key = str(args.access_key or "").strip()
    if not access_key:
        access_key = getpass.getpass("Target Access Key: ").strip()
    if not access_key:
        raise ValueError("the target access key is empty")

    status, login = request_json(
        base_url,
        "POST",
        "/api/user/login",
        {"access_key": access_key},
        args.timeout,
    )
    account = login.get("account") if isinstance(login.get("account"), dict) else {}
    name = str(account.get("access_key_id") or "").strip()
    if status != 200 or login.get("ok") is not True or not name:
        raise RuntimeError("the access key is invalid, already disabled, or could not be resolved")
    print("Resolved one active access key. The secret will not be displayed.")
    if args.dry_run:
        print("Dry run completed; the access key was not disabled.")
        return 0

    encoded_name = urllib.parse.quote(name, safe="")
    status, result = request_json(
        base_url,
        "POST",
        f"/api/admin/access-keys/{encoded_name}/revoke",
        {},
        args.timeout,
        admin_token,
    )
    if status != 200 or result.get("ok") is not True:
        raise RuntimeError(f"disable request failed with HTTP {status}: {result.get('error', 'unknown error')}")

    verify_status, _ = request_json(
        base_url,
        "POST",
        "/api/user/login",
        {"access_key": access_key},
        args.timeout,
    )
    if verify_status != 401:
        raise RuntimeError(
            f"the revoke request succeeded, but login verification returned HTTP {verify_status} instead of 401"
        )
    print("Access key disabled successfully; verification returned HTTP 401.")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ValueError, RuntimeError, OSError, json.JSONDecodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        raise SystemExit(1)
