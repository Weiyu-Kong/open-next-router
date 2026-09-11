#!/usr/bin/env python3
"""Create one ONR access key per email and export email-to-secret JSON.

The script is resumable: every returned secret is written atomically before
the balance is adjusted. It never prints access-key secrets or the admin token.
"""

from __future__ import annotations

import argparse
import csv
import hashlib
import json
import os
import re
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from decimal import Decimal
from pathlib import Path
from typing import Any


EMAIL_RE = re.compile(r"^[^@\s]+@[^@\s]+\.[^@\s]+$")


def load_json(path: Path) -> dict[str, Any]:
    with path.open(encoding="utf-8") as handle:
        value = json.load(handle)
    if not isinstance(value, dict):
        raise ValueError(f"{path} must contain a JSON object")
    return value


def atomic_write_json(path: Path, value: dict[str, str]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temp_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(value, handle, ensure_ascii=False, indent=2, sort_keys=True)
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


def read_emails(path: Path) -> list[str]:
    with path.open(encoding="utf-8-sig", newline="") as handle:
        reader = csv.DictReader(handle)
        if not reader.fieldnames or "email" not in reader.fieldnames:
            raise ValueError(f"{path} does not contain an email column")
        emails = [str(row.get("email", "")).strip().lower() for row in reader]
    invalid = [email for email in emails if not EMAIL_RE.fullmatch(email)]
    if invalid:
        raise ValueError(f"CSV contains {len(invalid)} invalid or empty email values")
    if len(emails) != len(set(emails)):
        raise ValueError("CSV contains duplicate email values")
    return emails


def key_name(email: str) -> str:
    local = re.sub(r"[^a-z0-9]+", "-", email.split("@", 1)[0].lower()).strip("-")
    local = (local or "user")[:32]
    digest = hashlib.sha256(email.encode()).hexdigest()[:12]
    return f"whitelist-{local}-{digest}"


class AdminAPI:
    def __init__(self, base_url: str, token: str, timeout: float) -> None:
        self.base_url = base_url.rstrip("/")
        self.token = token
        self.timeout = timeout

    def request(self, method: str, path: str, payload: dict[str, Any] | None = None) -> dict[str, Any]:
        body = None if payload is None else json.dumps(payload).encode()
        request = urllib.request.Request(
            self.base_url + path,
            data=body,
            method=method,
            headers={
                "Authorization": f"Bearer {self.token}",
                "Accept": "application/json",
                "Content-Type": "application/json",
            },
        )
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                result = json.load(response)
        except urllib.error.HTTPError as exc:
            detail = exc.read(4096).decode(errors="replace")
            raise RuntimeError(f"{method} {path} returned HTTP {exc.code}: {detail}") from exc
        except urllib.error.URLError as exc:
            raise RuntimeError(f"{method} {path} failed: {exc.reason}") from exc
        if not isinstance(result, dict):
            raise RuntimeError(f"{method} {path} returned a non-object response")
        return result


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=Path("config.json"))
    parser.add_argument("--api-base-url", help="Override the admin API URL from config.json")
    parser.add_argument("--csv", type=Path, default=Path("profiles_rows-3.csv"))
    parser.add_argument("--output", type=Path, default=Path("whitelist_access_keys.json"))
    parser.add_argument("--credit", type=Decimal, default=Decimal("200"))
    parser.add_argument("--providers", default="taotoken,ctyun")
    parser.add_argument("--timeout", type=float, default=30.0)
    parser.add_argument("--delay", type=float, default=0.05)
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    config = load_json(args.config)
    base_url = str(args.api_base_url or config.get("api", "")).strip()
    admin_token = str(config.get("admin_token", "")).strip()
    if not base_url or not admin_token:
        raise ValueError("config.json requires non-empty api and admin_token strings")
    if "://" not in base_url:
        base_url = "https://" + base_url
    emails = read_emails(args.csv)
    exported = load_json(args.output) if args.output.exists() else {}
    if not all(isinstance(k, str) and isinstance(v, str) for k, v in exported.items()):
        raise ValueError(f"{args.output} must be an email-to-secret JSON object")
    unknown = set(exported) - set(emails)
    if unknown:
        raise ValueError(f"{args.output} contains {len(unknown)} emails absent from the CSV")

    print(f"Validated {len(emails)} unique emails; {len(exported)} already exported.")
    if args.dry_run:
        print(f"Dry run: {len(emails) - len(exported)} access keys would be created.")
        return 0

    api = AdminAPI(base_url, admin_token, args.timeout)
    existing_response = api.request("GET", "/api/admin/access-keys")
    records = existing_response.get("records") or []
    existing_names = {str(record.get("name", "")) for record in records if isinstance(record, dict)}
    collisions = [email for email in emails if email not in exported and key_name(email) in existing_names]
    if collisions:
        raise RuntimeError(
            f"Refusing to continue: {len(collisions)} deterministic key names already exist "
            "but their secrets are absent from the output file"
        )

    for index, email in enumerate(emails, 1):
        name = key_name(email)
        if email not in exported:
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
                    "metadata": {"email": email, "source": args.csv.name},
                },
            )
            secret = str(response.get("secret", "")).strip()
            if not response.get("ok") or not secret:
                raise RuntimeError(f"creation failed for row {index}: {response.get('error', 'secret missing')}")
            exported[email] = secret
            atomic_write_json(args.output, exported)

        encoded_name = urllib.parse.quote(name, safe="")
        meter = api.request("GET", f"/api/admin/access-keys/{encoded_name}/meter")
        balance_data = meter.get("balance") or {}
        balance = Decimal(str(balance_data.get("available_balance", balance_data.get("balance", "0"))))
        delta = args.credit - balance
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
                    "idempotency_key": f"email-whitelist-200:{name}",
                },
            )
        if index == 1 or index % 25 == 0 or index == len(emails):
            print(f"Processed {index}/{len(emails)} accounts; secrets saved atomically.")
        if args.delay > 0:
            time.sleep(args.delay)

    os.chmod(args.output, 0o600)
    print(f"Completed {len(exported)} accounts. Output: {args.output} (mode 0600)")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ValueError, RuntimeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        raise SystemExit(1)
