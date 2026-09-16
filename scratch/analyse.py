"""Orchestrator true-cost analysis: time and money per unit of work.

Unit of work = a TURN: one user prompt handled to completion by the
orchestrator, including every tool call and subagent dispatch it made.

Measured exactly from the DB:
  - wall time      user prompt -> last assistant finish in that turn
  - gen time       sum of per-call (finished_at - created_at)
  - steps          assistant API calls in the turn
  - tool calls     tool results consumed in the turn

Modelled:
  - cost           the session's recorded cost (real provider usage,
                   minus subagent rollup) allocated across its turns in
                   proportion to per-call context+output token weight.

Subagent work is excluded from cost (child sessions carry their own
cost, which is subtracted from the parent) but included in wall time,
because the orchestrator blocks on it.
"""

import argparse
import collections
import datetime
import json

import numpy as np

# Prices Anvil used at runtime, $/1M tokens (in, out).
PRICE = {
    "claude-fable-5": (12.0, 60.0),
    "claude-opus-5": (6.0, 30.0),
    "claude-opus-4-6": (5.0, 25.0),
    "claude-opus-4-8": (6.0, 30.0),
    "claude-sonnet-5": (3.0, 15.0),
    "claude-sonnet-4-6": (3.0, 15.0),
    "claude-haiku-4-5-20251001": (1.0, 5.0),
}

# Fixed per-call context floor (system prompt + tool schemas + skills),
# measured from trivial single-exchange sessions.
SYSTEM_OVERHEAD = 30000


def chain_for(msgs, idx, byid):
    chain, cur, guard = [], msgs[idx].get("parent"), 0
    while cur and cur in byid and guard < 20000:
        chain.append(byid[cur])
        cur = byid[cur].get("parent")
        guard += 1
    chain.reverse()
    if len(chain) < idx * 0.5:
        chain = msgs[:idx]
    return chain


def build_turns(s):
    """Split one session into turns keyed off user messages."""
    msgs = s["msgs"]
    byid = {m["id"]: m for m in msgs}
    turns = []
    cur = None
    for i, m in enumerate(msgs):
        if m["role"] == "user":
            if cur:
                turns.append(cur)
            cur = {
                "start": m["created"],
                "prompt_tok": m["in_tok"],
                "steps": [],
                "tools": 0,
                "end": m["created"],
                "models": set(),
                "finishes": [],
                "weight": 0.0,
                "out_tok": 0,
                "ctx_tok": 0,
                "think": 0,
                "tool_names": collections.Counter(),
            }
            continue
        if cur is None:
            continue
        if m["role"] == "tool":
            cur["tools"] += 1
        elif m["role"] == "assistant" and m["mtype"] == "message":
            ctx = sum(x["in_tok"] + x["think_tok"]
                      for x in chain_for(msgs, i, byid)) + SYSTEM_OVERHEAD
            out = m["in_tok"] + m["think_tok"]
            model = m["model"] or ""
            p_in, p_out = PRICE.get(model, (5.0, 25.0))
            cur["weight"] += p_in / 1e6 * ctx + p_out / 1e6 * out
            cur["ctx_tok"] += ctx
            cur["out_tok"] += out
            cur["think"] += m["think_tok"]
            for nm in m.get("tools") or []:
                cur["tool_names"][nm] += 1
            cur["steps"].append(
                {
                    "created": m["created"],
                    "finished": m["finished"] or m["created"],
                    "model": model,
                }
            )
            if model:
                cur["models"].add(model)
            if m["finish"]:
                cur["finishes"].append(m["finish"])
            fin = m["finished"] or m["created"]
            cur["end"] = max(cur["end"], fin)
        elif m["role"] == "assistant":
            cur["end"] = max(cur["end"], m["finished"] or m["created"])
    if cur:
        turns.append(cur)
    return turns


def load(path="scratch/turns.json"):
    sessions = json.load(open(path))
    out = []
    for s in sessions:
        turns = build_turns(s)
        turns = [t for t in turns if t["steps"]]
        if not turns:
            continue
        orch_cost = max(0.0, s["cost"] - s["child_cost"])
        total_w = sum(t["weight"] for t in turns)
        for t in turns:
            share = (t["weight"] / total_w) if total_w > 0 else 0.0
            t["cost"] = orch_cost * share
            t["session"] = s["id"]
            t["title"] = s["title"]
            t["wall"] = max(0, t["end"] - t["start"])
            t["gen"] = sum(max(0, st["finished"] - st["created"])
                           for st in t["steps"])
            t["nsteps"] = len(t["steps"])
            t["model"] = (list(t["models"])[0]
                          if len(t["models"]) == 1 else "MIXED")
            t["ok"] = bool(t["finishes"]) and t["finishes"][-1] == "end_turn"
            t["date"] = s["created"]
        out.append((s, turns))
    return out


def pct(vals, q):
    return float(np.percentile(vals, q)) if len(vals) else float("nan")


def fmt_secs(v):
    if v != v:
        return "-"
    if v < 90:
        return f"{v:.0f}s"
    return f"{v/60:.1f}m"


def table(groups, title, cost_key="cost"):
    print(f"\n### {title}")
    hdr = (f"{'model':<18}{'turns':>6} | {'wall p50':>9}{'p95':>8}{'p99':>8}"
           f" | {'gen p50':>8}{'p95':>8}{'p99':>8}"
           f" | {'$ p50':>8}{'p95':>8}{'p99':>8}{'$ mean':>8}"
           f" | {'steps p50':>10}{'p95':>6}")
    print(hdr)
    print("-" * len(hdr))
    for model, ts in groups:
        if len(ts) < 20:
            continue
        wall = [t["wall"] for t in ts]
        gen = [t["gen"] for t in ts]
        cost = [t[cost_key] for t in ts]
        steps = [t["nsteps"] for t in ts]
        print(
            f"{model:<18}{len(ts):>6} | "
            f"{fmt_secs(pct(wall,50)):>9}{fmt_secs(pct(wall,95)):>8}"
            f"{fmt_secs(pct(wall,99)):>8} | "
            f"{fmt_secs(pct(gen,50)):>8}{fmt_secs(pct(gen,95)):>8}"
            f"{fmt_secs(pct(gen,99)):>8} | "
            f"{pct(cost,50):>8.3f}{pct(cost,95):>8.3f}{pct(cost,99):>8.3f}"
            f"{np.mean(cost):>8.3f} | "
            f"{pct(steps,50):>10.0f}{pct(steps,95):>6.0f}"
        )


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--models", nargs="*",
                    default=["claude-fable-5", "claude-opus-5",
                             "claude-opus-4-6"])
    args = ap.parse_args()

    data = load()
    all_turns = [t for _, ts in data for t in ts]
    print(f"root sessions: {len(data)}   orchestrator turns: {len(all_turns)}")
    tot = sum(max(0.0, s["cost"] - s["child_cost"]) for s, _ in data)
    child = sum(s["child_cost"] for s, _ in data)
    print(f"recorded orchestrator spend: ${tot:,.2f}   "
          f"subagent spend excluded: ${child:,.2f} "
          f"({child/(child+tot):.0%} of total)")

    sel = [t for t in all_turns if t["model"] in args.models]
    groups = [(m, [t for t in sel if t["model"] == m]) for m in args.models]

    table(groups, "All orchestrator turns")

    ok = [(m, [t for t in ts if t["ok"]]) for m, ts in groups]
    table(ok, "Completed turns only (finish=end_turn)")

    sub = [(m, [t for t in ts if t["ok"] and t["tools"] >= 1])
           for m, ts in groups]
    table(sub, "Substantive completed turns (>=1 tool call)")

    # Overlap window, to control for harness drift and task mix.
    lo, hi = None, None
    for m in args.models[:2]:
        ds = [t["date"] for t in sel if t["model"] == m]
        if not ds:
            continue
        lo = max(lo, min(ds)) if lo else min(ds)
        hi = min(hi, max(ds)) if hi else max(ds)
    if lo and hi and lo < hi:
        w = [(m, [t for t in ts if lo <= t["date"] <= hi and t["ok"]])
             for m, ts in groups]
        d1 = datetime.date.fromtimestamp(lo)
        d2 = datetime.date.fromtimestamp(hi)
        table(w, f"Completed turns in overlap window {d1} .. {d2}")

    print("\n### Work volume per completed turn (medians)")
    hdr = (f"{'model':<18}{'turns':>6}{'tool calls':>12}{'steps':>7}"
           f"{'out tok':>9}{'ctx tok':>10}{'$/step':>8}{'$/1k out':>10}")
    print(hdr)
    print("-" * len(hdr))
    for m, ts in ok:
        if len(ts) < 20:
            continue
        print(f"{m:<18}{len(ts):>6}"
              f"{np.median([t['tools'] for t in ts]):>12.0f}"
              f"{np.median([t['nsteps'] for t in ts]):>7.0f}"
              f"{np.median([t['out_tok'] for t in ts]):>9,.0f}"
              f"{np.median([t['ctx_tok'] for t in ts]):>10,.0f}"
              f"{np.median([t['cost']/t['nsteps'] for t in ts]):>8.3f}"
              f"{np.median([t['cost']/max(t['out_tok'],1)*1000 for t in ts]):>10.3f}")

    print("\n### Per-session totals (single-model root sessions)")
    hdr = (f"{'model':<18}{'sess':>6}{'turns p50':>10}"
           f"{'$ p50':>8}{'$ p95':>8}{'$ p99':>8}{'$ mean':>8}"
           f"{'wall p50':>10}{'wall p95':>10}")
    print(hdr)
    print("-" * len(hdr))
    per = collections.defaultdict(list)
    for s, ts in data:
        models = {t["model"] for t in ts}
        if len(models) != 1:
            continue
        m = models.pop()
        if m not in args.models:
            continue
        cost = max(0.0, s["cost"] - s["child_cost"])
        if cost <= 0.0005:
            continue
        per[m].append(
            {
                "cost": cost,
                "turns": len(ts),
                "wall": sum(t["wall"] for t in ts),
            }
        )
    for m in args.models:
        rs = per.get(m, [])
        if len(rs) < 10:
            continue
        c = [r["cost"] for r in rs]
        w = [r["wall"] for r in rs]
        print(f"{m:<18}{len(rs):>6}"
              f"{np.median([r['turns'] for r in rs]):>10.0f}"
              f"{pct(c,50):>8.2f}{pct(c,95):>8.2f}{pct(c,99):>8.2f}"
              f"{np.mean(c):>8.2f}"
              f"{fmt_secs(pct(w,50)):>10}{fmt_secs(pct(w,95)):>10}")


if __name__ == "__main__":
    main()
