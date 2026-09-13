#!/usr/bin/env python3
"""Credit active ONR accounts whose inferred usage exceeds a threshold."""

from __future__ import annotations

import argparse
import csv
import json
import os
import sys
import tempfile
from decimal import Decimal, InvalidOperation
from pathlib import Path
from typing import Any

from onr_admin_balance import (
    admin_settings,
    fetch_access_key_balances,
    list_access_keys,
    load_whitelist,
    request_json,
    resolve_whitelist_records,
)


AUDIT_FIELDS = [
    "access_key_name",
    "email",
    "account_id",
    "status",
    "balance_cny",
    "total_cny",
    "inferred_used_cny",
    "inferred_usage_ratio",
    "balance_cutoff_cny",
    "eligible",
    "reason",
]


def decimal_argument(value: str, option: str) -> Decimal:
    try:
        parsed = Decimal(value)
    except InvalidOperation as exc:
        raise ValueError(f"{option} must be a decimal number") from exc
    if not parsed.is_finite():
        raise ValueError(f"{option} must be finite")
    return parsed


def write_audit(path: Path, rows: list[dict[str, Any]]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="") as handle:
            writer = csv.DictWriter(handle, fieldnames=AUDIT_FIELDS)
            writer.writeheader()
            writer.writerows(rows)
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


def select_accounts(
    rows: list[dict[str, Any]], total: Decimal, usage_threshold: Decimal
) -> tuple[list[str], list[dict[str, Any]]]:
    cutoff = total * (Decimal("1") - usage_threshold)
    seen_accounts: set[str] = set()
    selected: list[str] = []
    audit: list[dict[str, Any]] = []
    for row in sorted(rows, key=lambda item: str(item["access_key_name"])):
        account_id = str(row["account_id"])
        balance_text = str(row["balance_cny"])
        reason = "eligible"
        eligible = True
        inferred_used = ""
        inferred_ratio = ""
        if row["status"] != "active":
            eligible, reason = False, "inactive key"
        elif row["error"] or not balance_text:
            eligible, reason = False, str(row["error"] or "balance unavailable")
        elif account_id in seen_accounts:
            eligible, reason = False, "shared account already evaluated"
        else:
            balance = Decimal(balance_text)
            inferred_used_value = total - balance
            inferred_ratio_value = inferred_used_value / total
            inferred_used = format(inferred_used_value, "f")
            inferred_ratio = format(inferred_ratio_value, "f")
            # "Usage above threshold" is deliberately strict. For 0.9 of 300,
            # this selects balances below 30, not balances equal to 30.
            eligible = balance < cutoff
            reason = "eligible" if eligible else "balance is not below cutoff"
        if (
            row["status"] == "active"
            and not row["error"]
            and balance_text
            and account_id not in seen_accounts
        ):
            seen_accounts.add(account_id)
        if eligible:
            selected.append(str(row["access_key_name"]))
        audit.append(
            {
                "access_key_name": row["access_key_name"],
                "email": row["email"],
                "account_id": account_id,
                "status": row["status"],
                "balance_cny": balance_text,
                "total_cny": format(total, "f"),
                "inferred_used_cny": inferred_used,
                "inferred_usage_ratio": inferred_ratio,
                "balance_cutoff_cny": format(cutoff, "f"),
                "eligible": "yes" if eligible else "no",
                "reason": reason,
            }
        )
    return selected, audit


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=Path("config.json"))
    parser.add_argument(
        "--input", type=Path, default=Path("whitelist_access_keys.json")
    )
    parser.add_argument("--api-base-url", help="Override config.json api")
    parser.add_argument("--usage-threshold", required=True, help="Fraction in [0,1], e.g. 0.9")
    parser.add_argument("--total", required=True, help="Reference total balance, e.g. 300")
    parser.add_argument("--amount", required=True, help="Fixed credit added to every selected account")
    parser.add_argument("--operation-id", help="Unique ID required with --apply; reuse only for a retry")
    parser.add_argument("--currency", default="CNY")
    parser.add_argument("--batch-size", type=int, default=50)
    parser.add_argument("--workers", type=int, default=8)
    parser.add_argument("--timeout", type=float, default=30.0)
    parser.add_argument("--audit-csv", type=Path, default=Path("credit_by_usage_audit.csv"))
    parser.add_argument("--apply", action="store_true", help="Actually issue credits; default is a dry run")
    args = parser.parse_args()

    usage_threshold = decimal_argument(args.usage_threshold, "--usage-threshold")
    total = decimal_argument(args.total, "--total")
    amount = decimal_argument(args.amount, "--amount")
    if usage_threshold < 0 or usage_threshold > 1:
        raise ValueError("--usage-threshold must be between 0 and 1")
    if total <= 0:
        raise ValueError("--total must be greater than zero")
    if amount <= 0:
        raise ValueError("--amount must be greater than zero")
    if args.workers < 1 or args.workers > 32:
        raise ValueError("--workers must be between 1 and 32")
    if args.batch_size < 1 or args.batch_size > 100:
        raise ValueError("--batch-size must be between 1 and 100")
    operation_id = str(args.operation_id or "").strip()
    if args.apply and (not operation_id or any(char.isspace() for char in operation_id)):
        raise ValueError("--operation-id is required with --apply and cannot contain whitespace")

    base_url, token = admin_settings(args.config, args.api_base_url)
    whitelist = load_whitelist(args.input)
    all_records = list_access_keys(base_url, token, args.timeout)
    records = resolve_whitelist_records(
        base_url, whitelist, all_records, args.timeout, args.workers
    )
    rows = fetch_access_key_balances(
        base_url, token, records, args.timeout, args.workers
    )
    selected, audit = select_accounts(rows, total, usage_threshold)
    write_audit(args.audit_csv, audit)
    cutoff = total * (Decimal("1") - usage_threshold)
    errors = sum(bool(row["error"]) for row in rows)
    print(
        f"Rule: inferred usage > {usage_threshold * 100}% of {total} {args.currency.upper()} "
        f"=> balance < {cutoff} {args.currency.upper()}"
    )
    print(
        f"Whitelist keys={len(rows)} unique eligible accounts={len(selected)} "
        f"balance_errors={errors} credit_per_account={amount} {args.currency.upper()}"
    )
    print(f"Audit CSV: {args.audit_csv}")
    if not args.apply:
        print("Dry run only: no balances were changed. Add --apply and --operation-id to execute.")
        return 0
    if not selected:
        print("No accounts matched; no balances were changed.")
        return 0

    completed = 0
    for offset in range(0, len(selected), args.batch_size):
        names = selected[offset : offset + args.batch_size]
        response = request_json(
            base_url,
            token,
            "POST",
            "/api/admin/access-keys/batch",
            args.timeout,
            {
                "action": "credit",
                "names": names,
                "amount": format(amount, "f"),
                "currency": args.currency.upper(),
                "idempotency_prefix": f"usage-threshold-credit:{operation_id}",
            },
        )
        results = response.get("results")
        if not isinstance(results, list):
            raise RuntimeError(f"batch at offset {offset} returned no results array")
        failed = [result for result in results if not isinstance(result, dict) or not result.get("ok")]
        if not response.get("ok") or len(results) != len(names) or failed:
            raise RuntimeError(
                f"credit batch incomplete at offset {offset}: expected={len(names)} "
                f"returned={len(results)} failed={len(failed)}"
            )
        completed += len(names)
        print(f"Credited {completed}/{len(selected)} accounts.")
    print(f"Completed: added {amount} {args.currency.upper()} to {completed} accounts.")
    print(f"Operation ID: {operation_id} (reuse only to retry this same operation).")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ValueError, RuntimeError, OSError, json.JSONDecodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        raise SystemExit(1)
