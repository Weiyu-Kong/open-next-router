#!/usr/bin/env python3
"""Plot whitelist account balance distributions as a dependency-free SVG."""

from __future__ import annotations

import argparse
import csv
import html
import math
import re
import statistics
from pathlib import Path


EMAIL_RE = re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}")


def read_excluded_emails(path: Path) -> set[str]:
    if not path.exists():
        return set()
    # Tolerate a trailing comma in the manually maintained JSON file.
    return {match.lower() for match in EMAIL_RE.findall(path.read_text(encoding="utf-8"))}


def read_balances(path: Path, excluded: set[str]) -> list[tuple[str, float]]:
    rows: list[tuple[str, float]] = []
    with path.open(encoding="utf-8-sig", newline="") as handle:
        reader = csv.DictReader(handle)
        if not reader.fieldnames or "balance_cny" not in reader.fieldnames:
            raise ValueError(f"{path} must contain a balance_cny column")
        for row in reader:
            email = str(row.get("email") or "").strip().lower()
            if email in excluded:
                continue
            identifier = email or str(row.get("access_key_name") or "").strip()
            if not identifier:
                continue
            balance = float(str(row.get("balance_cny") or "0"))
            if not math.isfinite(balance):
                raise ValueError(f"non-finite balance for {email}")
            rows.append((identifier, balance))
    if not rows:
        raise ValueError("no balances remain after filtering")
    return rows


def histogram(
    values: list[float], width: float, separate_value: float = 300.0
) -> list[tuple[str, int]]:
    """Return equal-width, half-open bins with one exact value separated."""
    lower = min(0.0, math.floor(min(values) / width) * width)
    regular_values = [
        value for value in values if not math.isclose(value, separate_value)
    ]
    regular_upper = (
        (math.floor(max(regular_values) / width) + 1) * width
        if regular_values
        else separate_value
    )
    upper = max(separate_value, regular_upper)
    if upper <= lower:
        upper = lower + width
    bins: list[tuple[str, int]] = []
    separate_added = False
    cursor = lower
    while cursor < upper:
        end = cursor + width
        if math.isclose(cursor, separate_value):
            bins.append(
                (
                    f"= {separate_value:g}",
                    sum(math.isclose(value, separate_value) for value in values),
                )
            )
            separate_added = True
        count = sum(
            cursor <= value < end and not math.isclose(value, separate_value)
            for value in values
        )
        if math.isclose(cursor, separate_value):
            label = f"({cursor:g},{end:g})"
        else:
            label = f"[{cursor:g},{end:g})"
        bins.append((label, count))
        cursor = end
    if not separate_added:
        bins.append(
            (
                f"= {separate_value:g}",
                sum(math.isclose(value, separate_value) for value in values),
            )
        )
    return bins


def esc(value: object) -> str:
    return html.escape(str(value), quote=True)


def bar_chart(
    title: str,
    labels: list[str],
    counts: list[int],
    x: int,
    y: int,
    width: int,
    height: int,
    total: int,
) -> str:
    left, right, top, bottom = 66, 18, 42, 62
    inner_w, inner_h = width - left - right, height - top - bottom
    maximum = max(counts) or 1
    slot = inner_w / len(counts)
    bar_w = slot * 0.68
    parts = [f'<g transform="translate({x},{y})">', f'<text x="0" y="18" class="title">{esc(title)}</text>']
    parts.append(f'<rect x="{left}" y="{top}" width="{inner_w}" height="{inner_h}" class="frame"/>')
    for tick_index in range(5):
        tick = maximum * tick_index / 4
        py = top + inner_h - inner_h * tick / maximum
        parts.append(f'<line x1="{left}" y1="{py:.2f}" x2="{left + inner_w}" y2="{py:.2f}" class="grid"/>')
        parts.append(f'<text x="{left - 9}" y="{py + 4:.2f}" text-anchor="end" class="tick">{round(tick):d}</text>')
    for index, (label, count) in enumerate(zip(labels, counts, strict=True)):
        bx = left + index * slot + (slot - bar_w) / 2
        bar_h = inner_h * count / maximum
        by = top + inner_h - bar_h
        percent = count / total * 100
        parts.append(f'<rect x="{bx:.2f}" y="{by:.2f}" width="{bar_w:.2f}" height="{bar_h:.2f}" class="bar"><title>{esc(label)}: {count} accounts ({percent:.2f}%)</title></rect>')
        parts.append(
            f'<text x="{bx + bar_w / 2:.2f}" y="{max(top + 12, by - 7):.2f}" '
            f'text-anchor="middle" class="value">{count}（{percent:.1f}%）</text>'
        )
        parts.append(f'<text x="{bx + bar_w / 2:.2f}" y="{top + inner_h + 19}" text-anchor="middle" class="tick">{esc(label)}</text>')
    parts.append(f'<text transform="translate(15,{top + inner_h / 2}) rotate(-90)" text-anchor="middle" class="axis">账户数</text>')
    parts.append('</g>')
    return "".join(parts)


def render_svg(
    rows: list[tuple[str, float]],
    excluded_count: int | None,
    title: str = "白名单账户余额分布",
) -> str:
    values = [balance for _, balance in rows]
    intervals = histogram(values, 100.0)
    hist = histogram(values, 25.0)
    total = len(values)
    mean = statistics.fmean(values)
    median = statistics.median(values)
    below_300 = sum(value < 300 for value in values)
    interval_svg = bar_chart(
        "余额区间分布（每档 100 CNY）",
        [label for label, _ in intervals],
        [count for _, count in intervals],
        34,
        126,
        1132,
        390,
        total,
    )
    histogram_svg = bar_chart(
        "余额直方图（每档 25 CNY）",
        [label for label, _ in hist],
        [count for _, count in hist],
        34,
        536,
        1132,
        390,
        total,
    )
    exclusion_note = (
        f" · 已排除 internel.json 中 {excluded_count} 个邮箱"
        if excluded_count is not None
        else ""
    )
    return f'''<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="960" viewBox="0 0 1200 960">
<style>
  .bg {{ fill: #f8fafc; }} .frame {{ fill: #fff; stroke: #cbd5e1; }}
  .grid {{ stroke: #e2e8f0; stroke-width: 1; }} .bar {{ fill: #21867a; }}
  text {{ fill: #17202a; font-family: "Noto Sans CJK SC", "Microsoft YaHei", sans-serif; }}
  .heading {{ font-size: 26px; font-weight: 700; }} .subtitle {{ font-size: 14px; fill: #64748b; }}
  .title {{ font-size: 17px; font-weight: 700; }} .metric {{ font-size: 17px; font-weight: 700; }}
  .metric-label {{ font-size: 12px; fill: #64748b; }} .tick {{ font-size: 11px; fill: #475569; }}
  .value {{ font-size: 12px; font-weight: 700; }}
  .axis {{ font-size: 12px; fill: #475569; }}
</style>
<rect width="1200" height="960" class="bg"/>
<text x="34" y="42" class="heading">{esc(title)}</text>
<text x="34" y="68" class="subtitle">账户数 {total}{exclusion_note}</text>
<g transform="translate(34,84)"><text class="metric">{mean:.2f} CNY</text><text y="20" class="metric-label">平均余额</text></g>
<g transform="translate(254,84)"><text class="metric">{median:.2f} CNY</text><text y="20" class="metric-label">中位数</text></g>
<g transform="translate(474,84)"><text class="metric">{min(values):.2f} CNY</text><text y="20" class="metric-label">最低余额</text></g>
<g transform="translate(694,84)"><text class="metric">{below_300}（{below_300 / total * 100:.2f}%）</text><text y="20" class="metric-label">余额低于 300</text></g>
{interval_svg}
{histogram_svg}
</svg>'''


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, default=Path("whitelist_usage_7d.csv"))
    parser.add_argument("--exclude", type=Path, default=Path("internel.json"))
    parser.add_argument("--output", type=Path, default=Path("whitelist_balance_distribution.svg"))
    args = parser.parse_args()
    excluded = read_excluded_emails(args.exclude)
    rows = read_balances(args.input, excluded)
    args.output.write_text(render_svg(rows, len(excluded)), encoding="utf-8")
    args.output.chmod(0o644)
    print(f"Generated {args.output} from {len(rows)} accounts; excluded {len(excluded)} emails.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
