#!/usr/bin/env python3
"""Shared helpers for ONR administrator balance scripts."""

from __future__ import annotations

import json
import urllib.error
import urllib.parse
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed
from decimal import Decimal, InvalidOperation
from pathlib import Path
from typing import Any


def load_object(path: Path) -> dict[str, Any]:
    with path.open(encoding="utf-8") as handle:
        value = json.load(handle)
    if not isinstance(value, dict):
        raise ValueError(f"{path} must contain a JSON object")
    return value


def admin_settings(config_path: Path, api_override: str | None) -> tuple[str, str]:
    config = load_object(config_path)
    base_url = str(api_override or config.get("api") or "").strip().rstrip("/")
    token = str(config.get("admin_token") or "").strip()
    if not base_url or not token:
        raise ValueError(f"{config_path} requires non-empty api and admin_token strings")
    if "://" not in base_url:
        base_url = "https://" + base_url
    return base_url, token


def request_json(
    base_url: str,
    token: str,
    method: str,
    path: str,
    timeout: float,
    payload: dict[str, Any] | None = None,
) -> dict[str, Any]:
    body = None if payload is None else json.dumps(payload).encode()
    headers = {
        "Accept": "application/json",
        "Content-Type": "application/json",
    }
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
    except OSError as exc:
        raise RuntimeError(f"{method} {path} failed: {exc}") from exc
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"{method} {path} returned invalid JSON") from exc
    if not isinstance(result, dict):
        raise RuntimeError(f"{method} {path} returned a non-object response")
    return result


def list_access_keys(base_url: str, token: str, timeout: float) -> list[dict[str, Any]]:
    response = request_json(base_url, token, "GET", "/api/admin/access-keys", timeout)
    records = response.get("records")
    if not isinstance(records, list):
        raise RuntimeError("admin access-key response does not contain a records array")
    clean = [record for record in records if isinstance(record, dict)]
    if len(clean) != len(records):
        raise RuntimeError("admin access-key response contains an invalid record")
    return clean


def load_whitelist(path: Path) -> dict[str, str]:
    raw = load_object(path)
    whitelist: dict[str, str] = {}
    secrets: set[str] = set()
    for raw_email, raw_secret in raw.items():
        email = str(raw_email).strip().lower()
        secret = str(raw_secret).strip()
        if not email or "@" not in email or not secret:
            raise ValueError(f"{path} contains an invalid email or empty access key")
        if email in whitelist:
            raise ValueError(f"{path} contains duplicate normalized emails")
        if secret in secrets:
            raise ValueError(f"{path} contains duplicate access keys")
        whitelist[email] = secret
        secrets.add(secret)
    if not whitelist:
        raise ValueError(f"{path} is empty")
    return whitelist


def record_email(record: dict[str, Any]) -> str:
    whitelist_email = str(record.get("whitelist_email") or "").strip().lower()
    if whitelist_email:
        return whitelist_email
    metadata = record.get("metadata") if isinstance(record.get("metadata"), dict) else {}
    email = str(metadata.get("email") or "").strip().lower()
    if not email and str(record.get("subject_type") or "").strip().lower() == "email":
        email = str(record.get("subject_id") or "").strip().lower()
    return email


def resolve_whitelist_records(
    base_url: str,
    whitelist: dict[str, str],
    all_records: list[dict[str, Any]],
    timeout: float,
    workers: int,
) -> list[dict[str, Any]]:
    """Resolve exactly the access-key secrets exported in the whitelist."""
    by_name = {
        str(record.get("name") or "").strip(): record
        for record in all_records
        if str(record.get("name") or "").strip()
    }
    by_email: dict[str, list[dict[str, Any]]] = {}
    for record in all_records:
        email = record_email(record)
        if email:
            by_email.setdefault(email, []).append(record)

    def resolve(item: tuple[str, str]) -> dict[str, Any]:
        email, secret = item
        resolution_error = ""
        record: dict[str, Any] | None = None
        try:
            login = request_json(
                base_url,
                "",
                "POST",
                "/api/user/login",
                timeout,
                {"access_key": secret},
            )
            account = login.get("account")
            name = (
                str(account.get("access_key_id") or "").strip()
                if isinstance(account, dict)
                else ""
            )
            if not login.get("ok") or not name:
                raise RuntimeError("login did not resolve an access-key ID")
            record = by_name.get(name)
            if record is None:
                raise RuntimeError("resolved access key is absent from the admin key list")
        except RuntimeError as exc:
            # Revoked keys cannot log in. A unique email record remains an
            # unambiguous fallback; ambiguous records are never guessed.
            candidates = by_email.get(email, [])
            if len(candidates) == 1:
                record = candidates[0]
                resolution_error = f"login failed; resolved by unique email: {exc}"
            else:
                return {
                    "name": "",
                    "status": "unresolved",
                    "subject_type": "email",
                    "subject_id": email,
                    "account_id": "",
                    "whitelist_email": email,
                    "resolution_error": str(exc),
                }
        result = dict(record)
        result["whitelist_email"] = email
        result["resolution_error"] = resolution_error
        return result

    resolved: list[dict[str, Any]] = []
    with ThreadPoolExecutor(max_workers=workers) as executor:
        futures = {
            executor.submit(resolve, item): item[0] for item in whitelist.items()
        }
        for future in as_completed(futures):
            resolved.append(future.result())
    resolved.sort(key=record_email)
    return resolved


def fetch_access_key_balances(
    base_url: str,
    token: str,
    records: list[dict[str, Any]],
    timeout: float,
    workers: int,
) -> list[dict[str, Any]]:
    """Read every key's wallet while retaining one output row per key."""
    account_counts: dict[str, int] = {}
    for record in records:
        account_id = str(record.get("account_id") or record.get("name") or "").strip()
        account_counts[account_id] = account_counts.get(account_id, 0) + 1

    def fetch(record: dict[str, Any]) -> dict[str, Any]:
        name = str(record.get("name") or "").strip()
        account_id = str(record.get("account_id") or name).strip()
        row: dict[str, Any] = {
            "access_key_name": name,
            "status": str(record.get("status") or ""),
            "email": record_email(record),
            "subject_type": str(record.get("subject_type") or ""),
            "subject_id": str(record.get("subject_id") or ""),
            "account_id": account_id,
            "balance_cny": "",
            "currency": "",
            "shared_account_key_count": account_counts.get(account_id, 1),
            "error": str(record.get("resolution_error") or ""),
        }
        if not name:
            row["error"] = row["error"] or "missing access-key name"
            return row
        try:
            encoded = urllib.parse.quote(name, safe="")
            meter = request_json(
                base_url,
                token,
                "GET",
                f"/api/admin/access-keys/{encoded}/meter",
                timeout,
            )
            balance = meter.get("balance")
            if not isinstance(balance, dict):
                raise RuntimeError("meter response does not contain a balance object")
            raw = balance.get("available_balance", balance.get("balance"))
            parsed = Decimal(str(raw))
            if not parsed.is_finite():
                raise InvalidOperation
            row["balance_cny"] = format(parsed, "f")
            row["currency"] = str(balance.get("currency") or "CNY").upper()
        except (RuntimeError, InvalidOperation, TypeError, ValueError) as exc:
            detail = str(exc)
            row["error"] = f"{row['error']}; {detail}" if row["error"] else detail
        return row

    rows: list[dict[str, Any]] = []
    with ThreadPoolExecutor(max_workers=workers) as executor:
        futures = {executor.submit(fetch, record): record for record in records}
        for future in as_completed(futures):
            rows.append(future.result())
    rows.sort(key=lambda row: row["access_key_name"])
    return rows
