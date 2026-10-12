#!/usr/bin/env python3
"""Create multiple unrestricted ONR access keys and save their secrets safely."""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import tempfile
from pathlib import Path
from typing import Any

from onr_admin_balance import admin_settings, list_access_keys, request_json


NAME_PART_RE = re.compile(r"^[A-Za-z0-9._-]+$")


def atomic_write_json(path: Path, value: list[dict[str, Any]]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            json.dump(value, handle, ensure_ascii=False, indent=2)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary, 0o600)
        os.replace(temporary, path)
    except BaseException:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass
        raise


def load_output(path: Path) -> list[dict[str, Any]]:
    if not path.exists():
        return []
    with path.open(encoding="utf-8") as handle:
        value = json.load(handle)
    if not isinstance(value, list) or not all(isinstance(item, dict) for item in value):
        raise ValueError(f"{path} must contain a JSON array of objects")
    names: set[str] = set()
    for item in value:
        name = str(item.get("name") or "").strip()
        secret = str(item.get("access_key") or "").strip()
        if not name or not secret:
            raise ValueError(f"{path} contains an entry without name or access_key")
        if name in names:
            raise ValueError(f"{path} contains duplicate access-key name {name!r}")
        names.add(name)
    return value


def key_names(prefix: str, count: int) -> list[str]:
    width = max(3, len(str(count)))
    return [f"{prefix}-{index:0{width}d}" for index in range(1, count + 1)]


def all_providers(base_url: str, token: str, timeout: float) -> list[str]:
    response = request_json(
        base_url, token, "GET", "/api/admin/provider-priority", timeout
    )
    providers = response.get("providers")
    if not isinstance(providers, list):
        raise RuntimeError("provider-priority response does not contain a providers array")
    clean: list[str] = []
    seen: set[str] = set()
    for raw in providers:
        provider = str(raw).strip().lower()
        if provider and provider not in seen:
            seen.add(provider)
            clean.append(provider)
    if not clean:
        raise RuntimeError("no providers are available")
    return clean


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--count", type=int, required=True, help="Number of keys to create")
    parser.add_argument("--prefix", default="batch-key", help="Key-name prefix")
    parser.add_argument(
        "--output", type=Path, default=Path("access_keys.json"), help="Secret output JSON"
    )
    parser.add_argument("--config", type=Path, default=Path("config.json"))
    parser.add_argument("--api-base-url", help="Override the admin API URL")
    parser.add_argument("--timeout", type=float, default=30.0)
    parser.add_argument(
        "--dry-run", action="store_true", help="Validate and show the plan without creating keys"
    )
    args = parser.parse_args()

    if args.count < 1 or args.count > 10000:
        raise ValueError("--count must be between 1 and 10000")
    prefix = str(args.prefix).strip()
    if not prefix or not NAME_PART_RE.fullmatch(prefix):
        raise ValueError("--prefix may contain only letters, digits, dot, underscore, and hyphen")
    if args.timeout <= 0:
        raise ValueError("--timeout must be greater than zero")

    base_url, token = admin_settings(args.config, args.api_base_url)
    providers = all_providers(base_url, token, args.timeout)
    expected_names = key_names(prefix, args.count)
    exported = load_output(args.output)
    exported_by_name = {str(item["name"]): item for item in exported}
    unexpected = set(exported_by_name) - set(expected_names)
    if unexpected:
        raise ValueError(
            f"{args.output} contains names outside the requested prefix/count: "
            + ", ".join(sorted(unexpected)[:5])
        )

    records = list_access_keys(base_url, token, args.timeout)
    existing_names = {
        str(record.get("name") or "").strip()
        for record in records
        if str(record.get("name") or "").strip()
    }
    lost_secrets = [
        name
        for name in expected_names
        if name in existing_names and name not in exported_by_name
    ]
    if lost_secrets:
        raise RuntimeError(
            "refusing to overwrite existing access keys whose secrets are absent from "
            f"{args.output}: " + ", ".join(lost_secrets[:5])
        )

    pending = [name for name in expected_names if name not in exported_by_name]
    print(
        f"Plan: requested={args.count} already_saved={len(exported)} "
        f"to_create={len(pending)} providers={','.join(providers)} models=unrestricted"
    )
    if args.dry_run:
        print("Dry run completed; no access keys were created.")
        return 0

    for position, name in enumerate(pending, 1):
        response = request_json(
            base_url,
            token,
            "POST",
            "/api/admin/access-keys",
            args.timeout,
            {
                "name": name,
                "subject_type": "api_key",
                "subject_id": name,
                "account_id": name,
                "route_policy_id": "",
                "allowed_providers": ",".join(providers),
                # An empty model allowlist means unrestricted model access.
                "allowed_models": "",
                "provider_key_bindings": {},
                "metadata": {"source": "scripts/create_access_keys.py"},
            },
        )
        secret = str(response.get("secret") or "").strip()
        if not response.get("ok") or not secret:
            raise RuntimeError(
                f"creation failed for {name}: {response.get('error') or 'secret missing'}"
            )
        item: dict[str, Any] = {
            "name": name,
            "access_key": secret,
            "allowed_providers": providers,
            "allowed_models": "*",
        }
        if response.get("pending"):
            item["provisioning"] = "pending"
            item["warning"] = str(response.get("error") or "provisioning is pending")
        exported_by_name[name] = item
        exported = [exported_by_name[key] for key in expected_names if key in exported_by_name]
        # Save every returned secret immediately; it cannot be recovered later.
        atomic_write_json(args.output, exported)
        if position == 1 or position % 25 == 0 or position == len(pending):
            print(f"Created {position}/{len(pending)} new keys; secrets saved atomically.")

    os.chmod(args.output, 0o600)
    print(f"Completed {len(exported)} access keys. Output: {args.output} (mode 0600)")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ValueError, RuntimeError, OSError, json.JSONDecodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        raise SystemExit(1)
