#!/usr/bin/env python3
"""Report whitelist account balances and threshold ratios."""

from __future__ import annotations

import argparse
import json
import sys
import urllib.error
import urllib.parse
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


def load_email_set(path: Path) -> set[str]:
    with path.open(encoding="utf-8") as handle:
        value = json.load(handle)
    if isinstance(value, list):
        return {str(item).strip().lower() for item in value if str(item).strip()}
    if isinstance(value, dict):
        return {str(item).strip().lower() for item in value if str(item).strip()}
    raise ValueError(f"{path} must contain an email array or object")


def api(base: str, token: str, method: str, path: str, timeout: float) -> dict[str, Any]:
    request = urllib.request.Request(
        base + path,
        method=method,
        headers={"Authorization": f"Bearer {token}", "Accept": "application/json"},
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            value = json.load(response)
    except urllib.error.HTTPError as exc:
        raise RuntimeError(f"{method} {path} returned HTTP {exc.code}") from exc
    except urllib.error.URLError as exc:
        raise RuntimeError(f"{method} {path} failed: {exc.reason}") from exc
    if not isinstance(value, dict):
        raise RuntimeError(f"{method} {path} returned invalid JSON")
    return value


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=Path("config.json"))
    parser.add_argument("--input", type=Path, default=Path("whitelist_access_keys.json"))
    parser.add_argument("--exclude-emails", type=Path, default=Path("internel.json"), help="Emails to remove before statistics")
    parser.add_argument("--timeout", type=float, default=30.0)
    parser.add_argument("--output", type=Path, help="Optional JSON report path")
    args = parser.parse_args()

    config = load_object(args.config)
    base = str(config.get("api") or "").strip().rstrip("/")
    token = str(config.get("admin_token") or "").strip()
    if not base or not token:
        raise ValueError("config.json requires api and admin_token")
    if "://" not in base:
        base = "https://" + base
    whitelist = load_object(args.input)
    emails = [str(email).strip().lower() for email in whitelist]
    if not emails or len(emails) != len(set(emails)):
        raise ValueError("input must contain unique email keys")

    excluded_emails = load_email_set(args.exclude_emails) if args.exclude_emails.exists() else set()
    emails = [email for email in emails if email not in excluded_emails]
    records_payload = api(base, token, "GET", "/api/admin/access-keys", args.timeout)
    records = records_payload.get("records") or []
    by_email: dict[str, list[dict[str, Any]]] = {}
    for record in records:
        if not isinstance(record, dict):
            continue
        metadata = record.get("metadata") if isinstance(record.get("metadata"), dict) else {}
        email = str(metadata.get("email") or "").strip().lower()
        if not email and str(record.get("subject_type") or "").lower() == "email":
            email = str(record.get("subject_id") or "").strip().lower()
        if email:
            by_email.setdefault(email, []).append(record)

    rows: list[dict[str, Any]] = []
    for email in emails:
        candidates = [r for r in by_email.get(email, []) if r.get("status") == "active"]
        if len(candidates) != 1:
            rows.append({"email": email, "balance": None, "status": "missing_or_ambiguous"})
            continue
        name = str(candidates[0].get("name") or "").strip()
        encoded = urllib.parse.quote(name, safe="")
        meter = api(base, token, "GET", f"/api/admin/access-keys/{encoded}/meter", args.timeout)
        balance_obj = meter.get("balance") or {}
        raw = balance_obj.get("available_balance", balance_obj.get("balance"))
        try:
            balance = Decimal(str(raw))
        except (InvalidOperation, TypeError):
            rows.append({"email": email, "balance": None, "status": "invalid_balance"})
            continue
        rows.append({"email": email, "balance": str(balance), "status": "active"})

    rows.sort(key=lambda row: (row["balance"] is None, Decimal(row["balance"]) if row["balance"] is not None else Decimal("0"), row["email"]))
    # Invalid, missing, ambiguous, and inactive keys are excluded entirely.
    known = [row for row in rows if row["balance"] is not None and row.get("status") == "active"]
    thresholds = {"lt_300": sum(Decimal(row["balance"]) < 300 for row in known), "lt_200": sum(Decimal(row["balance"]) < 200 for row in known), "lt_100": sum(Decimal(row["balance"]) < 100 for row in known), "lte_0": sum(Decimal(row["balance"]) <= 0 for row in known)}
    report_total = len(known)
    denominator = report_total
    if denominator <= 0:
        raise ValueError("ratio denominator must be positive")
    report = {"total_entries": report_total, "excluded_emails": sorted(excluded_emails), "denominator": denominator, "rows": known, "thresholds": {key: {"count": count, "ratio": round(count / denominator * 100, 4)} for key, count in thresholds.items()}}
    for row in known:
        print(f"{row['email']}\t{row['balance'] if row['balance'] is not None else 'N/A'}")
    print(f"\nTotal entries after exclusions: {report_total}; ratio denominator: {denominator}")
    for key, value in report["thresholds"].items():
        print(f"{key}: {value['count']} ({value['ratio']:.4f}%)")
    if args.output:
        args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        print(f"Report JSON: {args.output}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ValueError, RuntimeError, OSError, json.JSONDecodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        raise SystemExit(1)
