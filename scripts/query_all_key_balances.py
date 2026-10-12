#!/usr/bin/env python3
"""Query whitelist access-key balances and export CSV plus an SVG distribution."""

from __future__ import annotations

import argparse
import csv
import json
import os
import sys
import tempfile
from decimal import Decimal, InvalidOperation
from pathlib import Path

from onr_admin_balance import (
    admin_settings,
    fetch_access_key_balances,
    list_access_keys,
    load_whitelist,
    resolve_whitelist_records,
)
from plot_balance_distribution import read_excluded_emails, render_svg


FIELDS = [
    "access_key_name",
    "status",
    "email",
    "subject_type",
    "subject_id",
    "account_id",
    "balance_cny",
    "total_credit_cny",
    "inferred_consumed_cny",
    "currency",
    "shared_account_key_count",
    "error",
]


def write_csv_atomic(path: Path, rows: list[dict[str, object]]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="") as handle:
            writer = csv.DictWriter(handle, fieldnames=FIELDS)
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


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=Path("config.json"))
    parser.add_argument(
        "--input", type=Path, default=Path("whitelist_access_keys.json")
    )
    parser.add_argument("--api-base-url", help="Override config.json api")
    parser.add_argument(
        "--csv", type=Path, default=Path("whitelist_access_key_balances.csv")
    )
    parser.add_argument(
        "--chart",
        type=Path,
        default=Path("whitelist_access_key_balance_distribution.svg"),
    )
    parser.add_argument(
        "--consumption-chart",
        type=Path,
        default=Path("whitelist_access_key_consumption_distribution.svg"),
    )
    parser.add_argument(
        "--exclude",
        type=Path,
        default=Path("internel.json"),
        help="JSON/text file containing emails to exclude; default: internel.json",
    )
    parser.add_argument(
        "--total",
        default="500",
        help="Reference total credit used to infer consumption; default: 500",
    )
    parser.add_argument("--workers", type=int, default=8)
    parser.add_argument("--timeout", type=float, default=30.0)
    parser.add_argument(
        "--allow-partial",
        action="store_true",
        help="Exit successfully even when one or more balances cannot be read",
    )
    args = parser.parse_args()
    if args.workers < 1 or args.workers > 32:
        raise ValueError("--workers must be between 1 and 32")
    try:
        total = Decimal(args.total)
    except InvalidOperation as exc:
        raise ValueError("--total must be a decimal number") from exc
    if not total.is_finite() or total <= 0:
        raise ValueError("--total must be a positive finite decimal")

    base_url, token = admin_settings(args.config, args.api_base_url)
    whitelist = load_whitelist(args.input)
    excluded = read_excluded_emails(args.exclude)
    excluded_count = sum(email in excluded for email in whitelist)
    whitelist = {
        email: secret for email, secret in whitelist.items() if email not in excluded
    }
    if not whitelist:
        raise ValueError("no access keys remain after applying the exclusion list")
    all_records = list_access_keys(base_url, token, args.timeout)
    records = resolve_whitelist_records(
        base_url, whitelist, all_records, args.timeout, args.workers
    )
    rows = fetch_access_key_balances(
        base_url, token, records, args.timeout, args.workers
    )
    for row in rows:
        row["total_credit_cny"] = format(total, "f")
        balance = str(row["balance_cny"])
        row["inferred_consumed_cny"] = (
            format(total - Decimal(balance), "f") if balance else ""
        )
    write_csv_atomic(args.csv, rows)

    valid = [
        (str(row["access_key_name"]), float(str(row["balance_cny"])))
        for row in rows
        if row["balance_cny"] != ""
    ]
    if not valid:
        raise RuntimeError("no valid balances were returned; CSV was retained for diagnosis")
    args.chart.parent.mkdir(parents=True, exist_ok=True)
    args.chart.write_text(
        render_svg(valid, excluded_count, title="Access Key 余额分布"),
        encoding="utf-8",
    )
    args.chart.chmod(0o644)
    consumption = [
        (str(row["access_key_name"]), float(str(row["inferred_consumed_cny"])))
        for row in rows
        if row["inferred_consumed_cny"] != ""
    ]
    args.consumption_chart.parent.mkdir(parents=True, exist_ok=True)
    args.consumption_chart.write_text(
        render_svg(
            consumption,
            excluded_count,
            title=f"Access Key 总消耗分布（总额度 {format(total, 'f')} CNY）",
            metric_name="总消耗",
            separate_value=0.0,
            summary_direction="above",
        ),
        encoding="utf-8",
    )
    args.consumption_chart.chmod(0o644)

    failures = [row for row in rows if row["error"]]
    active = sum(row["status"] == "active" for row in rows)
    shared = sum(int(row["shared_account_key_count"]) > 1 for row in rows)
    print(
        f"Whitelist keys={len(rows)} active={active} balances={len(valid)} "
        f"errors={len(failures)} keys_on_shared_accounts={shared} excluded={excluded_count}"
    )
    print(f"CSV: {args.csv}")
    print(f"Chart: {args.chart}")
    print(f"Consumption chart: {args.consumption_chart}")
    if failures:
        for row in failures[:10]:
            print(
                f"warning: {row['access_key_name']}: {row['error']}",
                file=sys.stderr,
            )
        if len(failures) > 10:
            print(f"warning: {len(failures) - 10} more failures are in the CSV", file=sys.stderr)
        return 0 if args.allow_partial else 2
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ValueError, RuntimeError, OSError, json.JSONDecodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        raise SystemExit(1)
