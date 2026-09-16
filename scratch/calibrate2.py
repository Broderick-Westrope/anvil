"""Inspect calibration outliers and fit a robust token model."""

import json

import numpy as np

sessions = json.load(open("scratch/turns.json"))


def build_chain(msgs, target):
    by_id = {m["id"]: m for m in msgs}
    chain = []
    cur = target.get("parent")
    guard = 0
    while cur and cur in by_id and guard < 20000:
        chain.append(by_id[cur])
        cur = by_id[cur].get("parent")
        guard += 1
    chain.reverse()
    return chain


def ctx_tokens(chain):
    last_user = -1
    for i, m in enumerate(chain):
        if m["role"] == "user":
            last_user = i
    total = 0
    for i, m in enumerate(chain):
        total += m["in_tok"]
        if i > last_user:
            total += m["think_tok"]
    return total


rows = []
for s in sessions:
    msgs = s["msgs"]
    if s["prompt_tokens"] <= 0:
        continue
    idx = None
    for i in range(len(msgs) - 1, -1, -1):
        if msgs[i]["role"] == "assistant" and msgs[i]["finish"]:
            idx = i
            break
    if idx is None:
        continue
    chain = build_chain(msgs, msgs[idx])
    est = ctx_tokens(chain)
    rows.append(
        {
            "sid": s["id"],
            "title": s["title"][:38],
            "est": est,
            "act": s["prompt_tokens"],
            "chain": len(chain),
            "nmsgs": len(msgs),
            "linear_est": sum(m["in_tok"] for m in msgs[:idx]),
            "model": msgs[idx]["model"],
            "compaction": any(m["mtype"] == "compaction" for m in msgs),
        }
    )

rows.sort(key=lambda r: -(r["act"] / max(r["est"], 1)))
print("=== top 12 act/est outliers ===")
for r in rows[:12]:
    print(f"{r['act']/max(r['est'],1):8.1f}x act={r['act']:>8,} "
          f"est={r['est']:>8,} lin={r['linear_est']:>8,} "
          f"chain={r['chain']:>4}/{r['nmsgs']:<4} comp={r['compaction']!s:5} "
          f"{r['title']}")

print("\n=== chain length vs total messages (chain truncation check) ===")
short = [r for r in rows if r["chain"] < r["nmsgs"] * 0.3]
print(f"sessions where chain < 30% of messages: {len(short)}/{len(rows)}")

# Robust fit on sessions with a healthy chain.
good = [r for r in rows if r["chain"] >= r["nmsgs"] * 0.5 and r["est"] > 2000]
print(f"\nrobust-fit sessions: {len(good)}")
e = np.array([r["est"] for r in good], float)
a = np.array([r["act"] for r in good], float)
A = np.vstack([np.ones_like(e), e]).T
(c0, c1), *_ = np.linalg.lstsq(A, a, rcond=None)
pred = c0 + c1 * e
rel = np.abs(pred - a) / a
print(f"fit: act = {c0:,.0f} + {c1:.3f}*est")
print(f"rel err p50={np.percentile(rel,50):.1%} p90={np.percentile(rel,90):.1%}")

# Grid search overhead + scale minimising median relative error.
best = None
for oh in range(0, 90001, 2500):
    for sc in [x / 100 for x in range(80, 251, 5)]:
        p = oh + sc * e
        r = np.median(np.abs(p - a) / a)
        if best is None or r < best[0]:
            best = (r, oh, sc)
print(f"\ngrid best: median rel err {best[0]:.1%} "
      f"with overhead={best[1]:,} scale={best[2]}")
oh, sc = best[1], best[2]
p = oh + sc * e
rel = np.abs(p - a) / a
print(f"  rel err p25={np.percentile(rel,25):.1%} "
      f"p50={np.percentile(rel,50):.1%} p75={np.percentile(rel,75):.1%} "
      f"p90={np.percentile(rel,90):.1%}")
