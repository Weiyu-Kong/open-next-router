#!/usr/bin/env python3
"""Add 50 CNY to every access key in whitelist_access_keys.json."""

from __future__ import annotations

import argparse
import json
import sys
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any


def load_object(path: Path) -> dict[str, Any]:
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
    token: str,
    method: str,
    path: str,
    payload: dict[str, Any] | None,
    timeout: float,
) -> dict[str, Any]:
    body = None if payload is None else json.dumps(payload).encode()
    headers = {"Accept": "application/json", "Content-Type": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    request = urllib.request.Request(
        base_url + path,
        data=body,
        method=method,
        headers=headers,
    )
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


def whitelist_entries(path: Path) -> dict[str, str]:
    exported = load_object(path)
    if not exported:
        raise ValueError(f"{path} is empty")
    emails: list[str] = []
    secrets: list[str] = []
    for raw_email, raw_secret in exported.items():
        email = str(raw_email).strip().lower()
        secret = str(raw_secret).strip()
        if not email or "@" not in email or not secret:
            raise ValueError(f"{path} contains an invalid email or empty access key")
        emails.append(email)
        secrets.append(secret)
    if len(emails) != len(set(emails)):
        raise ValueError(f"{path} contains duplicate normalized emails")
    if len(secrets) != len(set(secrets)):
        raise ValueError(f"{path} contains duplicate access keys")
    return dict(zip(emails, secrets, strict=True))


def records_by_email(records: list[Any]) -> dict[str, list[dict[str, Any]]]:
    indexed: dict[str, list[dict[str, Any]]] = {}
    for record in records:
        if not isinstance(record, dict):
            continue
        metadata = record.get("metadata") if isinstance(record.get("metadata"), dict) else {}
        email = str(metadata.get("email") or "").strip().lower()
        if not email and str(record.get("subject_type") or "").strip().lower() == "email":
            email = str(record.get("subject_id") or "").strip().lower()
        if email:
            indexed.setdefault(email, []).append(record)
    return indexed


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=Path("config.json"))
    parser.add_argument("--input", type=Path, default=Path("whitelist_access_keys.json"))
    parser.add_argument("--operation-id", required=True, help="Unique ID; reuse only when retrying this operation")
    parser.add_argument("--api-base-url", help="Override the admin API URL from config.json")
    parser.add_argument("--batch-size", type=int, default=50)
    parser.add_argument("--timeout", type=float, default=60.0)
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    operation_id = args.operation_id.strip()
    if not operation_id or any(character.isspace() for character in operation_id):
        raise ValueError("--operation-id must be non-empty and contain no whitespace")
    if args.batch_size < 1 or args.batch_size > 100:
        raise ValueError("--batch-size must be between 1 and 100")
    whitelist = whitelist_entries(args.input)
    emails = list(whitelist)
    config = load_object(args.config)
    base_url = normalize_url(str(args.api_base_url or config.get("api", "")))
    token = str(config.get("admin_token") or "").strip()
    if not token:
        raise ValueError("config.json requires a non-empty admin_token")

    print(f"Validated {len(emails)} unique whitelist entries; credit=50 CNY each.")
    if args.dry_run:
        print("Dry run completed; no API requests were sent.")
        return 0

    response = request_json(base_url, token, "GET", "/api/admin/access-keys", None, args.timeout)
    records = response.get("records") or []
    if not isinstance(records, list):
        raise RuntimeError("admin access-key response did not include records")
    indexed = records_by_email(records)
    selected: list[dict[str, Any]] = []
    missing = 0
    inactive = 0
    ambiguous = 0
    unresolved_ambiguous = 0
    for email in emails:
        candidates = indexed.get(email, [])
        if not candidates:
            missing += 1
            continue
        active = [record for record in candidates if record.get("status") == "active"]
        if not active:
            inactive += 1
            continue
        if len(active) == 1:
            selected.append(active[0])
            continue

        # The export contains exactly one secret per email. Resolve that secret
        # only for ambiguous emails so exactly its corresponding record is
        # credited, instead of crediting every record sharing the email.
        ambiguous += 1
        try:
            login = request_json(
                base_url,
                "",
                "POST",
                "/api/user/login",
                {"access_key": whitelist[email]},
                args.timeout,
            )
        except RuntimeError:
            unresolved_ambiguous += 1
            continue
        account = login.get("account") if isinstance(login.get("account"), dict) else {}
        resolved_name = str(account.get("access_key_id") or "").strip()
        matches = [record for record in active if str(record.get("name") or "").strip() == resolved_name]
        if len(matches) == 1:
            selected.append(matches[0])
        else:
            unresolved_ambiguous += 1

    # Balance belongs to account_id, so deduplicate accounts as well as names.
    # This prevents two whitelist entries sharing an account from receiving
    # the same credit twice.
    names: list[str] = []
    seen_accounts: set[str] = set()
    duplicate_accounts = 0
    for record in selected:
        name = str(record.get("name") or "").strip()
        account_id = str(record.get("account_id") or name).strip()
        if not name or not account_id:
            continue
        if account_id in seen_accounts:
            duplicate_accounts += 1
            continue
        seen_accounts.add(account_id)
        names.append(name)
    print(
        "Resolved whitelist accounts: "
        f"selected={len(names)} missing_skipped={missing} inactive_skipped={inactive} "
        f"ambiguous_resolved={ambiguous - unresolved_ambiguous} "
        f"ambiguous_skipped={unresolved_ambiguous} duplicate_accounts_skipped={duplicate_accounts}."
    )
    if not names:
        raise RuntimeError("no eligible whitelist accounts remain after validation")

    completed = 0
    for offset in range(0, len(names), args.batch_size):
        batch = names[offset : offset + args.batch_size]
        result = request_json(
            base_url,
            token,
            "POST",
            "/api/admin/access-keys/batch",
            {
                "action": "credit",
                "names": batch,
                "amount": "50",
                "currency": "CNY",
                "idempotency_prefix": f"whitelist-credit-50:{operation_id}",
            },
            args.timeout,
        )
        rows = result.get("results") or []
        failed = [row for row in rows if not isinstance(row, dict) or not row.get("ok")]
        if not result.get("ok") or len(rows) != len(batch) or failed:
            raise RuntimeError(
                f"credit batch incomplete at offset {offset}: expected={len(batch)} "
                f"returned={len(rows)} failed={len(failed)}"
            )
        completed += len(batch)
        print(f"Credited {completed}/{len(names)} accounts.")

    print(f"Completed: added 50 CNY to {completed} whitelist accounts.")
    print(f"Operation ID: {operation_id} (reuse it only to retry this same operation).")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ValueError, RuntimeError, OSError, json.JSONDecodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        raise SystemExit(1)
