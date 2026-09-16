import collections
import numpy as np
from controls import enrich

MODELS = ["claude-fable-5", "claude-opus-5", "claude-opus-4-6"]
rows = [t for t in enrich() if t["model"] in MODELS]

print("### Turn outcome mix and wasted spend")
hdr = f"{'model':<17}{'turns':>7}{'end_turn':>10}{'canceled':>10}{'error':>8}{'maxtok':>8}{'other':>7}{'wasted $ share':>16}"
print(hdr); print("-"*len(hdr))
for m in MODELS:
    ts = [t for t in rows if t["model"] == m]
    if len(ts) < 20: continue
    c = collections.Counter(t["finishes"][-1] if t["finishes"] else "none" for t in ts)
    tot = sum(t["cost"] for t in ts)
    waste = sum(t["cost"] for t in ts if not t["ok"])
    n = len(ts)
    print(f"{m:<17}{n:>7}{c['end_turn']/n:>10.0%}{c['canceled']/n:>10.0%}"
          f"{c['error']/n:>8.1%}{c['max_tokens']/n:>8.1%}"
          f"{(n-c['end_turn']-c['canceled']-c['error']-c['max_tokens'])/n:>7.1%}"
          f"{waste/tot:>16.0%}")

print("\n### Effective cost per delivered turn (total spend / completed turns)")
for m in MODELS:
    ts = [t for t in rows if t["model"] == m]
    if len(ts) < 20: continue
    tot = sum(t["cost"] for t in ts)
    done = sum(1 for t in ts if t["ok"])
    walldone = [t["wall"] for t in ts if t["ok"]]
    print(f"  {m:<17} ${tot/done:>6.2f} per completed turn "
          f"(vs ${tot/len(ts):.2f} per attempted)  "
          f"median wall {np.median(walldone)/60:.1f}m")

print("\n### Immediate re-prompt rate (next turn < 45s after finish = correction)")
bysess = collections.defaultdict(list)
for t in rows: bysess[t["session"]].append(t)
for m in MODELS:
    quick = tot = 0
    for sid, ts in bysess.items():
        ts.sort(key=lambda x: x["seq"])
        for i, t in enumerate(ts[:-1]):
            if t["model"] != m or not t["ok"]: continue
            tot += 1
            if ts[i+1]["start"] - t["end"] < 45: quick += 1
    if tot > 20:
        print(f"  {m:<17} {quick/tot:.0%}  (n={tot})")
