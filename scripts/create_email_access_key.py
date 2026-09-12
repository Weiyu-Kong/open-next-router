#!/usr/bin/env python3
"""Create one email access key, set its balance, and append it to the export."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys
import tempfile
import urllib.parse
from decimal import Decimal, InvalidOperation
from pathlib import Path

from create_whitelist_access_keys import AdminAPI, load_json


EMAIL_RE = re.compile(r"^[^@\s]+@[^@\s]+\.[^@\s]+$")


def key_name(email: str) -> str:
    local = re.sub(r"[^a-z0-9]+", "-", email.split("@", 1)[0]).strip("-")
    digest = hashlib.sha256(email.encode()).hexdigest()[:12]
    return f"whitelist-{(local or 'user')[:32]}-{digest}"


def atomic_append(path: Path, email: str, secret: str, replace_existing: bool = False) -> None:
    exported = load_json(path) if path.exists() else {}
    if email in exported and not replace_existing:
        raise RuntimeError(f"{email} already exists in {path}")
    if any(not isinstance(key, str) or not isinstance(value, str) for key, value in exported.items()):
        raise ValueError(f"{path} must be an email-to-access-key JSON object")
    # Reinsert replacements at the end as well, while never retaining the
    # revoked secret in the export.
    exported.pop(email, None)
    exported[email] = secret
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temp_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            # Keep insertion order so the new email is appended at the end of
            # the JSON object instead of alphabetically reordering secrets.
            json.dump(exported, handle, ensure_ascii=False, indent=2)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temp_name, 0o600)
        os.replace(temp_name, path)
    except BaseException:
        try:
            os.unlink(temp_name)
        except FileNotFoundError:
            pass
        raise


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("email", help="Email address that owns the new access key")
    parser.add_argument("--config", type=Path, default=Path("config.json"))
    parser.add_argument("--output", type=Path, default=Path("whitelist_access_keys.json"))
    parser.add_argument("--credit", default="200", help="Target initial balance (default: 200)")
    parser.add_argument("--providers", default="taotoken,ctyun")
    parser.add_argument("--api-base-url", help="Override the admin API URL from config.json")
    parser.add_argument("--timeout", type=float, default=30.0)
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    email = args.email.strip().lower()
    if not EMAIL_RE.fullmatch(email):
        raise ValueError("email is invalid")
    try:
        target_credit = Decimal(args.credit)
    except InvalidOperation as exc:
        raise ValueError("--credit must be a decimal number") from exc
    if not target_credit.is_finite() or target_credit <= 0:
        raise ValueError("--credit must be greater than zero")

    config = load_json(args.config)
    base_url = str(args.api_base_url or config.get("api", "")).strip()
    token = str(config.get("admin_token", "")).strip()
    if not base_url or not token:
        raise ValueError("config.json requires non-empty api and admin_token strings")
    if "://" not in base_url:
        base_url = "https://" + base_url
    exported = load_json(args.output) if args.output.exists() else {}
    replacing_export = email in exported

    name = key_name(email)
    print(
        f"Validated email; providers={args.providers}; models=unrestricted; "
        f"target balance={target_credit} CNY."
    )
    if args.dry_run:
        print("Dry run completed; no API requests or file changes were made.")
        return 0

    api = AdminAPI(base_url, token, args.timeout)
    records_response = api.request("GET", "/api/admin/access-keys")
    records = [record for record in (records_response.get("records") or []) if isinstance(record, dict)]
    email_records = []
    for record in records:
        metadata = record.get("metadata") if isinstance(record.get("metadata"), dict) else {}
        record_email = str(metadata.get("email") or "").strip().lower()
        if not record_email and str(record.get("subject_type") or "").strip().lower() == "email":
            record_email = str(record.get("subject_id") or "").strip().lower()
        if record_email == email:
            email_records.append(record)

    active_records = [record for record in email_records if record.get("status") == "active"]
    if active_records:
        raise RuntimeError(
            "an active access key already exists for this email; refusing to create a duplicate"
        )
    if replacing_export and not email_records:
        raise RuntimeError(
            "the email exists in the export, but no matching revoked server record was found; "
            "refusing to replace it"
        )
    if replacing_export and any(record.get("status") != "revoked" for record in email_records):
        raise RuntimeError(
            "the existing email record is not fully revoked; refusing to replace its exported key"
        )

    existing_names = {str(record.get("name") or "").strip() for record in records}
    if name in existing_names:
        revision = 1
        while f"{name}-r{revision:02d}" in existing_names:
            revision += 1
        name = f"{name}-r{revision:02d}"
        print("A revoked key exists for this email; creating a new revision.")

    response = api.request(
        "POST",
        "/api/admin/access-keys",
        {
            "name": name,
            "subject_type": "email",
            "subject_id": email,
            "account_id": email,
            "allowed_providers": args.providers,
            "allowed_models": "",
            "provider_key_bindings": {},
            "metadata": {"email": email, "source": "single-email-script"},
        },
    )
    secret = str(response.get("secret", "")).strip()
    if not response.get("ok") or not secret:
        raise RuntimeError(f"access-key creation failed: {response.get('error', 'secret missing')}")

    # Persist the only copy of the returned secret before any later API call.
    atomic_append(args.output, email, secret, replace_existing=replacing_export)
    encoded_name = urllib.parse.quote(name, safe="")
    meter = api.request("GET", f"/api/admin/access-keys/{encoded_name}/meter")
    balance_data = meter.get("balance") or {}
    current = Decimal(str(balance_data.get("available_balance", balance_data.get("balance", "0"))))
    delta = target_credit - current
    if delta:
        operation = "credit" if delta > 0 else "debit"
        api.request(
            "POST",
            f"/api/admin/access-keys/{encoded_name}/balance",
            {
                "operation": operation,
                "amount": format(abs(delta), "f"),
                "currency": str(balance_data.get("currency", "CNY")),
                "reason": "Email whitelist initial quota",
                "idempotency_key": f"single-email-initial-credit:{name}:{target_credit}",
            },
        )

    verified = api.request("GET", f"/api/admin/access-keys/{encoded_name}/meter")
    verified_data = verified.get("balance") or {}
    final_balance = Decimal(str(verified_data.get("available_balance", verified_data.get("balance", "0"))))
    if final_balance != target_credit:
        raise RuntimeError(
            f"the key was created and exported, but balance verification failed: "
            f"expected {target_credit}, got {final_balance}"
        )
    os.chmod(args.output, 0o600)
    print(f"Created and exported one access key; verified balance={final_balance} CNY.")
    print(f"Output: {args.output} (mode 0600)")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ValueError, RuntimeError, OSError, json.JSONDecodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        raise SystemExit(1)
