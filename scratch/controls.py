"""Confound controls for the Fable-vs-Opus orchestrator comparison.

Three controls, weakest to strongest:

1. Stratify by user prompt size (exogenous: chosen before the model
   runs), so we are not just measuring "Opus got the hard prompts".
2. Composition: how much of each model's turn is delegation (task tool),
   which blocks wall time but moves cost into child sessions.
3. Paired within-session comparison across model_change boundaries -
   same session, same codebase, same working style, different model.
"""

import collections
import datetime
import json

import numpy as np

from analyse import PRICE, build_turns, chain_for  # noqa: F401

MODELS = ["claude-fable-5", "claude-opus-5", "claude-opus-4-6"]


def enrich(path="scratch/turns.json"):
    sessions = json.load(open(path))
    rows = []
    for s in sessions:
        turns = [t for t in build_turns(s) if t["steps"]]
        if not turns:
            continue
        orch = max(0.0, s["cost"] - s["child_cost"])
        tw = sum(t["weight"] for t in turns)
        for i, t in enumerate(turns):
            tal = t["tool_names"]
            t["cost"] = orch * (t["weight"] / tw if tw else 0)
            t["wall"] = max(0, t["end"] - t["start"])
            t["gen"] = sum(max(0, st["finished"] - st["created"])
                           for st in t["steps"])
            t["nsteps"] = len(t["steps"])
            t["model"] = (list(t["models"])[0]
                          if len(t["models"]) == 1 else "MIXED")
            t["ok"] = bool(t["finishes"]) and t["finishes"][-1] == "end_turn"
            t["date"] = s["created"]
            t["session"] = s["id"]
            t["seq"] = i
            t["subagents"] = tal.get("task", 0)
            t["mcp"] = sum(v for k, v in tal.items() if k.startswith("mcp"))
            t["reads"] = tal.get("view", 0)
            t["edits"] = (tal.get("edit", 0) + tal.get("multiedit", 0)
                          + tal.get("write", 0))
            t["bash"] = tal.get("bash", 0)
            rows.append(t)
    return rows


def q(v, p):
    return float(np.percentile(v, p)) if len(v) else float("nan")


def fs(v):
    return "-" if v != v else (f"{v:.0f}s" if v < 90 else f"{v/60:.1f}m")


def _main():
    rows = enrich()
    ok = [t for t in rows if t["ok"] and t["model"] in MODELS]
    print(f"completed orchestrator turns on tracked models: {len(ok)}")

    print("\n### Control 1: stratified by user prompt size (exogenous)")
    buckets = [(0, 40), (40, 150), (150, 600), (600, 10**9)]
    names = ["tiny <40tok", "small 40-150", "medium 150-600", "large >600"]
    hdr = (f"{'prompt bucket':<16}{'model':<18}{'n':>5}"
           f"{'wall p50':>10}{'wall p95':>10}{'gen p50':>9}"
           f"{'$ p50':>8}{'$ p95':>8}{'steps p50':>10}{'tools p50':>10}")
    print(hdr)
    print("-" * len(hdr))
    for (lo, hi), nm in zip(buckets, names):
        for m in MODELS:
            ts = [t for t in ok
                  if m == t["model"] and lo <= t["prompt_tok"] < hi]
            if len(ts) < 15:
                continue
            print(f"{nm:<16}{m:<18}{len(ts):>5}"
                  f"{fs(q([t['wall'] for t in ts],50)):>10}"
                  f"{fs(q([t['wall'] for t in ts],95)):>10}"
                  f"{fs(q([t['gen'] for t in ts],50)):>9}"
                  f"{q([t['cost'] for t in ts],50):>8.2f}"
                  f"{q([t['cost'] for t in ts],95):>8.2f}"
                  f"{q([t['nsteps'] for t in ts],50):>10.0f}"
                  f"{q([t['tools'] for t in ts],50):>10.0f}")
        print()

    print("### Control 2: turn composition (medians per completed turn)")
    hdr = (f"{'model':<18}{'n':>5}{'think tok':>11}{'subagents':>11}"
           f"{'mcp':>6}{'reads':>7}{'edits':>7}{'bash':>6}"
           f"{'% turns w/ subagent':>21}{'gen/step':>10}")
    print(hdr)
    print("-" * len(hdr))
    for m in MODELS:
        ts = [t for t in ok if t["model"] == m]
        if len(ts) < 20:
            continue
        print(f"{m:<18}{len(ts):>5}"
              f"{np.median([t['think'] for t in ts]):>11,.0f}"
              f"{np.median([t['subagents'] for t in ts]):>11.0f}"
              f"{np.median([t['mcp'] for t in ts]):>6.0f}"
              f"{np.median([t['reads'] for t in ts]):>7.0f}"
              f"{np.median([t['edits'] for t in ts]):>7.0f}"
              f"{np.median([t['bash'] for t in ts]):>6.0f}"
              f"{np.mean([t['subagents'] > 0 for t in ts]):>20.0%}"
              f"{np.median([t['gen']/t['nsteps'] for t in ts]):>10.1f}")

    print("\n### Control 3: paired within-session model switches")
    bysess = collections.defaultdict(list)
    for t in rows:
        bysess[t["session"]].append(t)
    pairs = []
    for sid, ts in bysess.items():
        ts.sort(key=lambda x: x["seq"])
        ms = [t["model"] for t in ts if t["model"] in MODELS]
        if len(set(ms)) < 2:
            continue
        for a in MODELS:
            for b in MODELS:
                if a >= b:
                    continue
                ta = [t for t in ts if t["model"] == a and t["ok"]]
                tb = [t for t in ts if t["model"] == b and t["ok"]]
                if ta and tb:
                    pairs.append((sid, a, b, ta, tb))
    print(f"sessions containing a tracked model switch: "
          f"{len({p[0] for p in pairs})}")
    agg = collections.defaultdict(lambda: {"wall": [], "cost": [],
                                           "steps": [], "gen": []})
    for sid, a, b, ta, tb in pairs:
        key = (a, b)
        for f, name in (("wall", "wall"), ("cost", "cost"),
                        ("nsteps", "steps"), ("gen", "gen")):
            va = np.median([t[f] for t in ta])
            vb = np.median([t[f] for t in tb])
            if va > 0 and vb > 0:
                agg[key][name].append(va / vb)
    for (a, b), d in agg.items():
        if len(d["wall"]) < 5:
            continue
        print(f"\n  {a} vs {b}  (n={len(d['wall'])} sessions)")
        for k in ("wall", "gen", "steps", "cost"):
            v = d[k]
            print(f"    median ratio {k:<6} = {np.median(v):.2f}x "
                  f"(p25 {np.percentile(v,25):.2f} p75 {np.percentile(v,75):.2f})")

    print("\n### Time trend: is Opus 5 just a later, heavier-harness era?")
    hdr = f"{'month':<10}{'model':<18}{'n':>5}{'wall p50':>10}{'steps p50':>10}{'$ p50':>8}"
    print(hdr)
    print("-" * len(hdr))
    mon = collections.defaultdict(list)
    for t in ok:
        mon[(datetime.date.fromtimestamp(t["date"]).strftime("%Y-%m"),
             t["model"])].append(t)
    for (mo, m), ts in sorted(mon.items()):
        if len(ts) < 25:
            continue
        print(f"{mo:<10}{m:<18}{len(ts):>5}"
              f"{fs(q([t['wall'] for t in ts],50)):>10}"
              f"{q([t['nsteps'] for t in ts],50):>10.0f}"
              f"{q([t['cost'] for t in ts],50):>8.2f}")


if __name__ == "__main__":
    _main()
