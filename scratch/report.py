"""Final consolidated Fable-5 vs Opus-5 orchestrator cost/time report."""

import collections
import datetime

import numpy as np

from controls import enrich

MODELS = ["claude-fable-5", "claude-opus-5", "claude-opus-4-6"]
rows = enrich()
ok = [t for t in rows if t["ok"] and t["model"] in MODELS]


def q(v, p):
    return float(np.percentile(v, p)) if len(v) else float("nan")


def fs(v):
    if v != v:
        return "-"
    return f"{v:.0f}s" if v < 90 else f"{v / 60:.1f}m"


def block(title, sel, minn=20):
    print(f"\n### {title}")
    hdr = (f"{'model':<17}{'turns':>6} | {'wall p50':>9}{'p95':>8}{'p99':>8}"
           f" | {'gen p50':>8}{'p95':>8}{'p99':>8}"
           f" | {'$ p50':>7}{'p95':>8}{'p99':>8}"
           f" | {'steps':>6}{'tools':>6}")
    print(hdr)
    print("-" * len(hdr))
    for m in MODELS:
        ts = [t for t in sel if t["model"] == m]
        if len(ts) < minn:
            continue
        w = [t["wall"] for t in ts]
        g = [t["gen"] for t in ts]
        c = [t["cost"] for t in ts]
        print(f"{m:<17}{len(ts):>6} | "
              f"{fs(q(w, 50)):>9}{fs(q(w, 95)):>8}{fs(q(w, 99)):>8} | "
              f"{fs(q(g, 50)):>8}{fs(q(g, 95)):>8}{fs(q(g, 99)):>8} | "
              f"{q(c, 50):>7.2f}{q(c, 95):>8.2f}{q(c, 99):>8.2f} | "
              f"{q([t['nsteps'] for t in ts], 50):>6.0f}"
              f"{q([t['tools'] for t in ts], 50):>6.0f}")


print(f"completed orchestrator turns: {len(ok)}")
block("A. All completed turns, all time", ok)

jul_aug = [t for t in ok
           if "2026-07" <= datetime.date.fromtimestamp(t["date"]).strftime("%Y-%m") <= "2026-08"]
block("B. Head-to-head, Jul+Aug 2026 only (same harness era)", jul_aug)

heavy = [t for t in ok if t["tools"] >= 3]
block("C. Multi-step turns only (>=3 tool calls)", heavy)

print("\n### D. Composition per completed turn (means)")
hdr = (f"{'model':<17}{'n':>5}{'tools':>7}{'steps':>7}{'subagt':>7}"
       f"{'% deleg':>9}{'reads':>7}{'edits':>7}{'bash':>6}{'mcp':>6}"
       f"{'s/call':>8}{'$/tool':>8}")
print(hdr)
print("-" * len(hdr))
for m in MODELS:
    ts = [t for t in ok if t["model"] == m]
    if len(ts) < 20:
        continue
    print(f"{m:<17}{len(ts):>5}"
          f"{np.mean([t['tools'] for t in ts]):>7.1f}"
          f"{np.mean([t['nsteps'] for t in ts]):>7.1f}"
          f"{np.mean([t['subagents'] for t in ts]):>7.2f}"
          f"{np.mean([t['subagents'] > 0 for t in ts]):>8.0%} "
          f"{np.mean([t['reads'] for t in ts]):>7.1f}"
          f"{np.mean([t['edits'] for t in ts]):>7.1f}"
          f"{np.mean([t['bash'] for t in ts]):>6.1f}"
          f"{np.mean([t['mcp'] for t in ts]):>6.1f}"
          f"{np.median([t['gen'] / t['nsteps'] for t in ts]):>8.1f}"
          f"{np.median([t['cost'] / max(t['tools'], 1) for t in ts]):>8.3f}")

print("\n### E. Paired within-session switches, by direction")
bysess = collections.defaultdict(list)
for t in rows:
    bysess[t["session"]].append(t)
res = collections.defaultdict(lambda: collections.defaultdict(list))
for sid, ts in bysess.items():
    ts.sort(key=lambda x: x["seq"])
    seq = [t for t in ts if t["ok"] and t["model"] in MODELS]
    if len({t["model"] for t in seq}) < 2:
        continue
    first = seq[0]["model"]
    fab = [t for t in seq if t["model"] == "claude-fable-5"]
    opu = [t for t in seq if t["model"] == "claude-opus-5"]
    if not (fab and opu):
        continue
    direction = "fable->opus" if first == "claude-fable-5" else "opus->fable"
    for f, nm in (("wall", "wall"), ("gen", "gen"),
                  ("nsteps", "steps"), ("cost", "cost")):
        a = np.median([t[f] for t in fab])
        b = np.median([t[f] for t in opu])
        if a > 0 and b > 0:
            res[direction][nm].append(a / b)
            res["both"][nm].append(a / b)
for d, dd in res.items():
    n = len(dd["wall"])
    if n < 4:
        continue
    parts = " ".join(f"{k}={np.median(v):.2f}x" for k, v in dd.items())
    print(f"  {d:<12} n={n:<3} fable/opus5 medians: {parts}")

print("\n### F. Cost/time to finish a whole session (single-model roots)")
import json  # noqa: E402

sessions = json.load(open("scratch/turns.json"))
per = collections.defaultdict(list)
for s in sessions:
    ts = [t for t in rows if t["session"] == s["id"]]
    if not ts:
        continue
    ms = {t["model"] for t in ts}
    if len(ms) != 1:
        continue
    m = ms.pop()
    if m not in MODELS:
        continue
    cost = max(0.0, s["cost"] - s["child_cost"])
    if cost <= 0.0005:
        continue
    per[m].append({"cost": cost, "turns": len(ts),
                   "gen": sum(t["gen"] for t in ts),
                   "child": s["child_cost"]})
hdr = (f"{'model':<17}{'sess':>6}{'turns p50':>10}{'$ p50':>8}{'$ p95':>9}"
       f"{'$ p99':>9}{'gen p50':>9}{'gen p95':>9}{'subagent $ p50':>15}")
print(hdr)
print("-" * len(hdr))
for m in MODELS:
    rs = per.get(m, [])
    if len(rs) < 10:
        continue
    c = [r["cost"] for r in rs]
    g = [r["gen"] for r in rs]
    print(f"{m:<17}{len(rs):>6}"
          f"{np.median([r['turns'] for r in rs]):>10.0f}"
          f"{q(c, 50):>8.2f}{q(c, 95):>9.2f}{q(c, 99):>9.2f}"
          f"{fs(q(g, 50)):>9}{fs(q(g, 95)):>9}"
          f"{np.median([r['child'] for r in rs]):>15.2f}")

print("\n### G. Blended reality: what a turn costs including subagents")
for m in MODELS:
    rs = per.get(m, [])
    if len(rs) < 10:
        continue
    tot = sum(r["cost"] for r in rs)
    ch = sum(r["child"] for r in rs)
    turns = sum(r["turns"] for r in rs)
    print(f"  {m:<17} orch ${tot / turns:.2f}/turn  "
          f"+ subagents ${ch / turns:.2f}/turn  "
          f"= ${(tot + ch) / turns:.2f}/turn  "
          f"(subagents {ch / (tot + ch):.0%} of spend)")
