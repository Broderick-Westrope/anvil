import collections, json
import numpy as np
sessions = json.load(open("scratch/turns.json"))
PRICE = {"claude-fable-5": (12.0, 60.0), "claude-opus-5": (6.0, 30.0),
         "claude-opus-4-6": (5.0, 25.0), "claude-sonnet-5": (3.0, 15.0)}

def chain_for(msgs, idx, byid):
    chain, cur, guard = [], msgs[idx].get("parent"), 0
    while cur and cur in byid and guard < 20000:
        chain.append(byid[cur]); cur = byid[cur].get("parent"); guard += 1
    chain.reverse()
    return chain, len(chain) >= idx * 0.5

rows = []
badchain = 0
for s in sessions:
    msgs = s["msgs"]; byid = {m["id"]: m for m in msgs}
    models = {m["model"] for m in msgs if m["role"]=="assistant" and m["model"]}
    if len(models)!=1: continue
    model = models.pop()
    if model not in PRICE: continue
    orch = s["cost"] - s["child_cost"]
    if orch <= 0.0005: continue
    p_in, p_out = PRICE[model]
    calls = [i for i,m in enumerate(msgs) if m["role"]=="assistant" and m["finish"]]
    cum = 0; ok = True
    for i in calls:
        ch, good = chain_for(msgs, i, byid)
        if not good: ch = msgs[:i]; ok = False
        cum += sum(m["in_tok"]+m["think_tok"] for m in ch)
    if not ok: badchain += 1
    out = sum(m["in_tok"]+m["think_tok"] for m in msgs if m["role"]=="assistant")
    # add fixed system overhead per call
    for oh_name, oh in (("oh0", 0), ("oh20k", 20000), ("oh35k", 35000)):
        pass
    rows.append(dict(model=model, cost=orch, cum=cum, out=out,
                     calls=len(calls), p_in=p_in, p_out=p_out))

print(f"sessions={len(rows)} chain-fallback={badchain}")
by = collections.defaultdict(list)
for r in rows: by[r["model"]].append(r)
print(f"\n{'model':<20}{'n':>4}{'cost/(Pin*cum)':>16}{'cost/(Pin*(cum+35k*calls))':>28}")
for m, rs in sorted(by.items(), key=lambda kv:-len(kv[1])):
    a = np.median([r["cost"]/(r["p_in"]/1e6*r["cum"]) for r in rs if r["cum"]>0])
    b = np.median([r["cost"]/(r["p_in"]/1e6*(r["cum"]+35000*r["calls"])) for r in rs if r["cum"]>0])
    print(f"{m:<20}{len(rs):>4}{a:>16.2f}{b:>28.2f}")

# best single global model: cost ~ Pin*(scale*cum + oh*calls) + Pout*out
best=None
cum=np.array([r["cum"] for r in rows],float); calls=np.array([r["calls"] for r in rows],float)
out=np.array([r["out"] for r in rows],float); cost=np.array([r["cost"] for r in rows],float)
pin=np.array([r["p_in"] for r in rows],float); pout=np.array([r["p_out"] for r in rows],float)
for sc in [x/10 for x in range(5,41)]:
    for oh in range(0,120001,5000):
        pred = pin/1e6*(sc*cum + oh*calls) + pout/1e6*out
        r = np.median(np.abs(pred-cost)/cost)
        if best is None or r<best[0]: best=(r,sc,oh)
r,sc,oh = best
pred = pin/1e6*(sc*cum + oh*calls) + pout/1e6*out
rel = np.abs(pred-cost)/cost
print(f"\nbest: scale={sc} overhead/call={oh:,} -> median rel err {r:.1%}")
print(f"  rel err p25={np.percentile(rel,25):.1%} p50={np.percentile(rel,50):.1%} p75={np.percentile(rel,75):.1%} p90={np.percentile(rel,90):.1%}")
for m in by:
    idx=[i for i,rr in enumerate(rows) if rr["model"]==m]
    print(f"  {m:<20} median pred/act = {np.median(pred[idx]/cost[idx]):.2f}")
