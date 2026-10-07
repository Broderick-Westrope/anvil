#!/usr/bin/env python3
"""Analyse past /review runs stored in the Anvil database.

Produces the numbers behind docs/review-subagent-analysis.md, split by
review-setup period.

Usage:
  scripts/review-analysis.py                      # full report
  scripts/review-analysis.py --dump-audit A       # print audit lines for a period
  scripts/review-analysis.py --db path/to/anvil.db
"""

import argparse
import json
import math
import os
import re
import sqlite3
from collections import Counter, defaultdict
from datetime import datetime, timezone

SWITCH = int(datetime(2026, 8, 31, tzinfo=timezone.utc).timestamp())

PERIODS = {
    "A": {
        ("rev", "claude-sonnet-4-6"): "Sonnet",
        ("rev", "claude-opus-4-6"): "Opus",
        ("conv", "claude-sonnet-4-6"): "Convention",
    },
    "B": {
        ("rev", "claude-sonnet-5"): "Sonnet",
        ("rev", "claude-opus-5"): "Opus",
        ("conv", "claude-sonnet-5"): "Convention",
    },
    "C": {
        ("rev", "claude-sonnet-5"): "Sonnet",
        ("rev", "gpt-6-astra"): "Astra",
        ("conv", "claude-sonnet-5"): "Convention (default)",
        ("conv", "gpt-6-astra"): "Convention (Astra)",
    },
    "D": {
        ("rev", "claude-sonnet-5"): "Sonnet",
        ("rev", "claude-opus-5-5"): "Opus",
        ("conv", "claude-sonnet-5"): "Convention (default)",
        ("conv", "gpt-6.1-sol"): "Convention (Sol)",
    },
    "E": {
        ("rev", "claude-sonnet-5"): "Sonnet",
        ("rev", "claude-opus-5-5"): "Opus",
        ("conv", "claude-sonnet-5"): "Convention (default)",
        ("conv", "claude-opus-5-5"): "Convention (Opus)",
    },
}
ORDER = ["Baseline", *PERIODS]

LABEL = r"(?:Sonnet|Opus|Astra|Convention(?: \((?:default|Astra|Opus|Sol)\))?)"
TAG = re.compile(r"\[(" + LABEL + r"(?: \+ " + LABEL + r")*)\]")
MAIN_SECTIONS = (
    "Critical Issues",
    "Important Issues",
    "Suggestions",
    "Developer Experience Issues",
    "Product & UX Issues",
    "Documentation Updates Needed",
)
CRIT_IMP = ("Critical Issues", "Important Issues")


def strip_provider(model):
    return re.sub(r"^(anthropic|openai)/", "", model or "")


def child_tid(session_id):
    return session_id.split("$$", 1)[1]


def load_calls(cur):
    calls = []
    rows = cur.execute(
        "SELECT session_id, id, created_at, parts FROM messages WHERE role='assistant' "
        "AND parts LIKE '%tool_call%' AND parts LIKE '%subagent_type%'"
    )
    for sid, mid, created, parts in rows:
        for part in json.loads(parts):
            if part.get("type") != "tool_call":
                continue
            data = part["data"]
            try:
                inp = json.loads(data.get("input") or "{}")
            except json.JSONDecodeError:
                continue
            agent = inp.get("subagent_type")
            if agent in ("reviewer", "convention-reviewer"):
                calls.append(
                    {
                        "sid": sid,
                        "mid": mid,
                        "created": created,
                        "tid": data["id"],
                        "agent": "conv" if agent == "convention-reviewer" else "rev",
                        "requested": inp.get("model") or "default",
                    }
                )
    return calls


def load_children(cur):
    child = {}
    for cid, cost, prompt_tokens in cur.execute(
        "SELECT id, cost, prompt_tokens FROM sessions WHERE id LIKE '%$$%'"
    ):
        child[child_tid(cid)] = {"id": cid, "cost": cost, "prompt_tokens": prompt_tokens}
    for cid, model in cur.execute(
        "SELECT session_id, model FROM messages WHERE role='assistant' AND model IS NOT NULL "
        "AND session_id LIKE '%$$%' GROUP BY session_id"
    ):
        if child_tid(cid) in child:
            child[child_tid(cid)]["model"] = strip_provider(model)
    for cid, dur in cur.execute(
        "SELECT session_id, MAX(COALESCE(finished_at, updated_at)) - MIN(created_at) "
        "FROM messages WHERE session_id LIKE '%$$%' GROUP BY session_id"
    ):
        if child_tid(cid) in child:
            child[child_tid(cid)]["dur"] = dur
    return child


def baseline_label(call):
    if call["agent"] == "conv":
        return "Convention"
    requested = call["requested"].lower()
    if "sonnet" in requested:
        return "Sonnet"
    if "opus" in requested:
        return "Opus"
    return None


def classify_runs(calls, child):
    runs = defaultdict(list)
    for call in calls:
        runs[(call["sid"], call["mid"])].append(call)

    period_of, labels_of = {}, {}
    for key, cs in runs.items():
        if cs[0]["created"] < SWITCH:
            labels = {c["tid"]: baseline_label(c) for c in cs}
            if sorted(labels.values(), key=str) == ["Convention", "Opus", "Sonnet"]:
                period_of[key], labels_of[key] = "Baseline", labels
            continue
        sig = {c["tid"]: (c["agent"], child.get(c["tid"], {}).get("model", "?")) for c in cs}
        for period, mapping in PERIODS.items():
            if sorted(sig.values()) == sorted(mapping):
                period_of[key] = period
                labels_of[key] = {tid: mapping[s] for tid, s in sig.items()}
                break

    keys_by_session = defaultdict(set)
    for key in runs:
        keys_by_session[key[0]].add(key)
    pure = {}
    for sid, keys in keys_by_session.items():
        periods = {period_of.get(k) for k in keys}
        if None not in periods and len(periods) == 1:
            pure[sid] = periods.pop()
    return runs, period_of, labels_of, pure


def audit_bucket(section, line):
    sec = section.lower()
    if "~~" in line or re.search(
        r"reject|refut|invalid|incorrect|declin|wrong|non-issue|no-action|no action|"
        r"do not action|dropped|unverified|speculative|false|downgrad|partial|adjust",
        sec,
    ):
        return "REJ"
    if re.search(r"confirm|valid|must-fix|verified", sec):
        return "CONF"
    if "audit" in sec:
        low = line.lower()
        if re.search(r"\*\*valid|confirmed|correct as reported|stands|upheld", low):
            return "CONF"
        if re.search(
            r"invalid|rejected|false positive|misread|not a bug|deliberate|intentional|"
            r"by design|refuted",
            low,
        ):
            return "REJ"
    return None


def tagged_lines(cur, sid):
    seen, section = set(), ""
    for (parts,) in cur.execute(
        "SELECT parts FROM messages WHERE session_id=? AND role='assistant' ORDER BY created_at",
        (sid,),
    ):
        for part in json.loads(parts):
            if part.get("type") != "text":
                continue
            for line in part["data"].get("text", "").split("\n"):
                heading = re.match(r"^#+ (.*)", line)
                if heading:
                    section = re.sub(r"[^\x20-\x7e]", "", heading.group(1)).strip()
                match = TAG.search(line)
                if not match or line in seen:
                    continue
                seen.add(line)
                labels = frozenset(x.strip() for x in match.group(1).split(" + "))
                yield section, line, labels


def load_findings(cur, pure):
    findings = defaultdict(list)
    for sid, period in pure.items():
        for section, line, labels in tagged_lines(cur, sid):
            findings[period].append(
                {"sid": sid, "section": section, "labels": labels, "audit": audit_bucket(section, line)}
            )
    return findings


def verdict(cur, child, tid):
    info = child.get(tid)
    if not info:
        return None
    for (parts,) in cur.execute(
        "SELECT parts FROM messages WHERE session_id=? AND role='assistant' ORDER BY created_at DESC",
        (info["id"],),
    ):
        for part in json.loads(parts):
            if part.get("type") == "text":
                match = re.search(r"REQUEST CHANGES|APPROVE", part["data"].get("text", ""), re.I)
                if match:
                    return "RC" if "REQUEST" in match.group(0).upper() else "AP"
    return None


def percentile(values, p):
    values = sorted(values)
    return values[max(0, math.ceil(len(values) * p / 100) - 1)] if values else None


def mean(values):
    return sum(values) / len(values) if values else 0


def pearson(xs, ys):
    if len(xs) < 4:
        return float("nan")
    mx, my = mean(xs), mean(ys)
    sx = math.sqrt(sum((x - mx) ** 2 for x in xs))
    sy = math.sqrt(sum((y - my) ** 2 for y in ys))
    if not sx or not sy:
        return float("nan")
    return sum((x - mx) * (y - my) for x, y in zip(xs, ys)) / (sx * sy)


def report_period(cur, period, run_keys, labels_of, child, findings):
    labels = sorted({l for k in run_keys for l in labels_of[k].values()})
    print(f"\n==================== {period} ====================")
    print(f"clean runs: {len(run_keys)} | sessions: {len({k[0] for k in run_keys})}")
    if not run_keys:
        return

    main = [f for f in findings if f["section"].startswith(MAIN_SECTIONS)]
    print(f"main-section findings: {len(main)} | caught by all: "
          f"{sum(1 for f in main if len(f['labels']) == len(labels))}")

    per_label = {}
    for label in labels:
        tids = [t for k in run_keys for t, l in labels_of[k].items() if l == label]
        total = [f for f in main if label in f["labels"]]
        solo = [f for f in total if len(f["labels"]) == 1]
        solo_ci = [f for f in solo if f["section"].startswith(CRIT_IMP)]
        per_label[label] = {
            "cost": mean([child[t]["cost"] for t in tids if t in child]),
            "findings": len(total) / len(run_keys),
            "solo_ci": len(solo_ci) / len(run_keys),
        }
        print(f"  {label:22s} findings={len(total):4d} solo={len(solo):4d} "
              f"({100 * len(solo) / max(1, len(total)):.0f}%) "
              f"per-run={len(total) / len(run_keys):.1f} "
              f"solo-crit/imp per-run={len(solo_ci) / len(run_keys):.2f}")

    crit = Counter(" + ".join(sorted(f["labels"])) for f in main
                   if f["section"].startswith("Critical Issues"))
    print("  criticals:", dict(crit.most_common()))

    unique_ci, with_ci = defaultdict(set), set()
    for f in main:
        if f["section"].startswith(CRIT_IMP):
            with_ci.add(f["sid"])
            if len(f["labels"]) == 1:
                unique_ci[next(iter(f["labels"]))].add(f["sid"])
    print(f"  sessions with crit/imp: {len(with_ci)} | sessions with unique crit/imp:",
          {l: len(unique_ci[l]) for l in labels})

    audited = [f for f in findings if f["audit"]]
    audited_sessions = {f["sid"] for f in audited}
    print(f"  audited sessions: {len(audited_sessions)}")
    for label in labels:
        base = [f for f in main if label in f["labels"] and f["sid"] in audited_sessions]
        rejected = [f for f in audited if label in f["labels"] and f["audit"] == "REJ"]
        ruled = [f for f in audited if label in f["labels"]]
        confirmed = sum(1 for f in ruled if f["audit"] == "CONF")
        rej_pct = f" ({100 * len(rejected) / len(base):.0f}%)" if base else ""
        conf_pct = f" ({100 * confirmed / len(ruled):.0f}%)" if ruled else ""
        print(f"  {label:22s} explicitly rejected {len(rejected)}/{len(base)}{rej_pct} | "
              f"confirmed of ruled-on {confirmed}/{len(ruled)}{conf_pct}")

    verdicts = []
    for k in run_keys:
        v = {labels_of[k][t]: verdict(cur, child, t) for t in labels_of[k]}
        if None not in v.values():
            verdicts.append(v)
    split = [v for v in verdicts if len(set(v.values())) > 1]
    sole = Counter()
    for v in split:
        blockers = [l for l, x in v.items() if x == "RC"]
        if len(blockers) == 1:
            sole[blockers[0]] += 1
    print(f"  verdicts: {len(verdicts)} runs | unanimous AP "
          f"{sum(1 for v in verdicts if set(v.values()) == {'AP'})} | unanimous RC "
          f"{sum(1 for v in verdicts if set(v.values()) == {'RC'})} | split {len(split)} | "
          f"sole RC {dict(sole)}")

    for label in labels:
        tids = [t for k in run_keys for t, l in labels_of[k].items() if l == label and t in child]
        costs = [child[t]["cost"] for t in tids]
        durs = [child[t]["dur"] for t in tids if child[t].get("dur") is not None]
        tokens = [child[t]["prompt_tokens"] for t in tids]
        print(f"  {label:22s} cost avg ${mean(costs):.2f} total ${sum(costs):.0f} | "
              f"dur p50 {percentile(durs, 50)}s p90 {percentile(durs, 90)}s "
              f"p99 {percentile(durs, 99)}s avg {mean(durs):.0f}s | "
              f"prompt tokens p50 {percentile(tokens, 50)}")

    run_cost = [sum(child[t]["cost"] for t in labels_of[k] if t in child) for k in run_keys]
    run_wall = [max((child[t].get("dur") or 0) for t in labels_of[k] if t in child) for k in run_keys]
    print(f"  per-run cost avg ${mean(run_cost):.2f} | wall-clock (slowest reviewer) "
          f"p50 {percentile(run_wall, 50)}s p90 {percentile(run_wall, 90)}s")

    repos = Counter(
        cur.execute("SELECT working_dir FROM sessions WHERE id=?", (k[0],)).fetchone()[0].split("/")[-1]
        for k in run_keys
    )
    print("  top repos:", repos.most_common(5))

    anchor = "Opus" if "Opus" in per_label else None
    if anchor:
        for label, stats in per_label.items():
            if label == anchor:
                continue
            a = per_label[anchor]
            print(f"  {label} relative to {anchor} in same runs: "
                  f"cost {stats['cost'] / a['cost']:.2f}x | "
                  f"findings {stats['findings'] / max(a['findings'], 1e-9):.2f}x | "
                  f"unique crit/imp {stats['solo_ci'] / max(a['solo_ci'], 1e-9):.2f}x")

    if period in ("C", "D", "E"):
        conv = sorted(l for l in labels if l.startswith("Convention"))
        if len(conv) == 2:
            both = sum(1 for f in main if set(conv) <= f["labels"])
            only = [sum(1 for f in main if c in f["labels"] and (set(conv) - {c}).isdisjoint(f["labels"]))
                    for c in conv]
            print(f"  convention overlap: both={both} {conv[0]}-only={only[0]} {conv[1]}-only={only[1]}")

    durations = defaultdict(list)
    for k in run_keys:
        for t, l in labels_of[k].items():
            if t in child and child[t].get("dur") is not None:
                durations[(k[0], l)].append(child[t]["dur"])
    outcomes = defaultdict(lambda: [0, 0])
    for f in audited:
        for l in f["labels"]:
            outcomes[(f["sid"], l)][0 if f["audit"] == "CONF" else 1] += 1
    pairs = sorted((mean(durations[k]), *outcomes[k]) for k in outcomes if k in durations)
    if len(pairs) >= 6:
        print(f"  duration vs audit: pairs={len(pairs)} "
              f"r(dur,valid)={pearson([p[0] for p in pairs], [p[1] for p in pairs]):.2f} "
              f"r(dur,invalid)={pearson([p[0] for p in pairs], [p[2] for p in pairs]):.2f}")
        third = len(pairs) // 3
        for i in range(3):
            chunk = pairs[i * third:(i + 1) * third] if i < 2 else pairs[2 * third:]
            valid, invalid = sum(p[1] for p in chunk), sum(p[2] for p in chunk)
            print(f"    tercile {i + 1} ({chunk[0][0]:.0f}-{chunk[-1][0]:.0f}s) valid={valid} "
                  f"invalid={invalid} precision={100 * valid / max(1, valid + invalid):.0f}%")


def dump_audit(cur, pure, period):
    for sid, p in pure.items():
        if p != period:
            continue
        for section, line, _ in tagged_lines(cur, sid):
            bucket = audit_bucket(section, line)
            if bucket:
                print(f"{sid[:8]} | {bucket} | {section[:40]} | {line[:150]}")


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--db", default=os.path.expanduser("~/.local/share/anvil/anvil.db"))
    parser.add_argument("--dump-audit", metavar="PERIOD", choices=ORDER,
                        help="print classified audit lines for one period instead of the report")
    args = parser.parse_args()

    cur = sqlite3.connect(f"file:{args.db}?mode=ro", uri=True).cursor()
    child = load_children(cur)
    runs, period_of, labels_of, pure = classify_runs(load_calls(cur), child)

    if args.dump_audit:
        dump_audit(cur, pure, args.dump_audit)
        return

    findings = load_findings(cur, pure)
    for period in ORDER:
        keys = [k for k, p in period_of.items() if p == period and k[0] in pure]
        report_period(cur, period, keys, labels_of, child, findings[period])


if __name__ == "__main__":
    main()
