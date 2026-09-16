"""Work out what the recorded session cost is actually made of.

Anvil prices claude-fable-5 / claude-opus-5 with cache prices of 0, so
the recorded cost only captures whatever the provider reported as
uncached InputTokens plus OutputTokens. This script tests whether
recorded cost is explained by output tokens alone, by uncached input
alone, or by a mix.
"""

import collections
import json

import numpy as np

sessions = json.load(open("scratch/turns.json"))

# Prices Anvil used at runtime (providers.json), $/1M tokens.
PRICE = {
    "claude-fable-5": (12.0, 60.0),
    "claude-opus-5": (6.0, 30.0),
    "claude-opus-4-6": (5.0, 25.0),
    "claude-opus-4-8": (6.0, 30.0),
    "claude-sonnet-5": (3.0, 15.0),
    "claude-haiku-4-5-20251001": (1.0, 5.0),
}


def chain_for(msgs, idx):
    by_id = {m["id"]: m for m in msgs}
    chain, cur, guard = [], msgs[idx].get("parent"), 0
    while cur and cur in by_id and guard < 20000:
        chain.append(by_id[cur])
        cur = by_id[cur].get("parent")
        guard += 1
    chain.reverse()
    if len(chain) < idx * 0.5:
        chain = msgs[:idx]
    return chain


rows = []
for s in sessions:
    msgs = s["msgs"]
    models = {m["model"] for m in msgs if m["role"] == "assistant" and m["model"]}
    if len(models) != 1:
        continue
    model = models.pop()
    if model not in PRICE:
        continue
    orch_cost = s["cost"] - s["child_cost"]
    if orch_cost <= 0.0005:
        continue
    p_in, p_out = PRICE[model]

    out_tok = sum(
        m["in_tok"] + m["think_tok"] for m in msgs if m["role"] == "assistant"
    )
    # Uncached increment per call: tokens added since the previous call.
    calls = [i for i, m in enumerate(msgs)
             if m["role"] == "assistant" and m["finish"]]
    incr = 0
    prev_ctx = 0
    for i in calls:
        chain = chain_for(msgs, i)
        c = sum(m["in_tok"] for m in chain)
        incr += max(0, c - prev_ctx)
        prev_ctx = c
    # Full cumulative context across all calls (what cache reads cover).
    cum_ctx = 0
    for i in calls:
        cum_ctx += sum(m["in_tok"] for m in chain_for(msgs, i))

    rows.append(
        {
            "model": model,
            "cost": orch_cost,
            "out_tok": out_tok,
            "incr": incr,
            "cum_ctx": cum_ctx,
            "calls": len(calls),
            "out_only": p_out / 1e6 * out_tok,
            "in_only": p_in / 1e6 * incr,
            "mix": p_out / 1e6 * out_tok + p_in / 1e6 * incr,
        }
    )

print(f"single-model root sessions with cost: {len(rows)}\n")
by = collections.defaultdict(list)
for r in rows:
    by[r["model"]].append(r)

hdr = (f"{'model':<26}{'n':>4}{'  cost/out_only':>16}"
       f"{'  cost/in_only':>15}{'  cost/mix':>11}")
print(hdr)
print("-" * len(hdr))
for model, rs in sorted(by.items(), key=lambda kv: -len(kv[1])):
    def med(f):
        v = [f(r) for r in rs if f(r) is not None]
        return np.median(v) if v else float("nan")
    print(f"{model:<26}{len(rs):>4}"
          f"{med(lambda r: r['cost']/r['out_only'] if r['out_only'] else None):>16.2f}"
          f"{med(lambda r: r['cost']/r['in_only'] if r['in_only'] else None):>15.2f}"
          f"{med(lambda r: r['cost']/r['mix'] if r['mix'] else None):>11.2f}")

print("\nIf cost/out_only ~ 1.0, recorded cost is output-token driven.")
print("Correlation of recorded cost with each candidate basis:")
for name in ("out_only", "in_only", "mix", "cum_ctx"):
    a = np.array([r["cost"] for r in rows])
    b = np.array([r[name] for r in rows], float)
    print(f"  {name:<9} r={np.corrcoef(a, b)[0,1]:.3f}")
