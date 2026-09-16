"""Calibrate the token estimator against stored session prompt_tokens.

session.prompt_tokens is a snapshot of the LAST API call's
InputTokens + CacheReadTokens, i.e. the full context size at the end of
the session. Comparing our reconstructed context size at that same point
tells us how good the chars/4 estimator is and how large the fixed
system-prompt + tool-schema overhead is.
"""

import json
import statistics

import numpy as np

sessions = json.load(open("scratch/turns.json"))


def chain_context(msgs, idx):
    """Reconstruct input context tokens for the API call at msgs[idx].

    Walks the parent_message_id chain so branched sessions are priced
    against their own ancestry rather than wall-clock order.
    """
    by_id = {m["id"]: m for m in msgs}
    target = msgs[idx]
    chain = []
    cur = target.get("parent")
    guard = 0
    while cur and cur in by_id and guard < 10000:
        chain.append(by_id[cur])
        cur = by_id[cur].get("parent")
        guard += 1
    chain.reverse()
    # Thinking from prior turns is not resent; thinking within the
    # current turn is. Find the last user message in the chain.
    last_user = -1
    for i, m in enumerate(chain):
        if m["role"] == "user":
            last_user = i
    total = 0
    for i, m in enumerate(chain):
        total += m["in_tok"]
        if i > last_user:
            total += m["think_tok"]
    return total, len(chain)


rows = []
for s in sessions:
    msgs = s["msgs"]
    if s["prompt_tokens"] <= 0:
        continue
    # Last assistant message with a finish part is the last API call.
    idx = None
    for i in range(len(msgs) - 1, -1, -1):
        if msgs[i]["role"] == "assistant" and msgs[i]["finish"]:
            idx = i
            break
    if idx is None:
        continue
    est, depth = chain_context(msgs, idx)
    if est <= 0:
        continue
    rows.append((est, s["prompt_tokens"], depth, s["completion_tokens"],
                 msgs[idx]["think_tok"] + msgs[idx]["in_tok"], s["id"]))

print(f"calibration sessions: {len(rows)}")
est = np.array([r[0] for r in rows], dtype=float)
act = np.array([r[1] for r in rows], dtype=float)

# Fit act = a + b*est via least squares.
A = np.vstack([np.ones_like(est), est]).T
(a, b), *_ = np.linalg.lstsq(A, act, rcond=None)
print(f"fit: prompt_tokens = {a:,.0f} + {b:.3f} * est_tokens")
pred = a + b * est
resid = (pred - act) / act
print(f"median abs rel error: {statistics.median(abs(resid)):.1%}")
print(f"p90 abs rel error:    {np.percentile(abs(resid), 90):.1%}")

# Also report the naive ratio (no scaling), to see raw estimator bias.
ratio = act / est
print(f"\nact/est ratio: p10={np.percentile(ratio,10):.2f} "
      f"p50={np.percentile(ratio,50):.2f} p90={np.percentile(ratio,90):.2f}")
print(f"absolute gap (act-est): p50={np.percentile(act-est,50):,.0f} "
      f"p90={np.percentile(act-est,90):,.0f}")

# Output-token sanity check.
oa = np.array([r[3] for r in rows], dtype=float)
oe = np.array([r[4] for r in rows], dtype=float)
m = (oa > 0) & (oe > 0)
print(f"\noutput tokens act/est: p50={np.percentile(oa[m]/oe[m],50):.2f} "
      f"n={m.sum()}")
