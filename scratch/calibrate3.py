"""Test token-model variants against stored prompt_tokens."""

import json

import numpy as np

sessions = json.load(open("scratch/turns.json"))


def chain_for(msgs, idx):
    """Ancestor chain of msgs[idx]; falls back to chronological order
    when parent links are missing (pre-tree-migration sessions)."""
    by_id = {m["id"]: m for m in msgs}
    chain = []
    cur = msgs[idx].get("parent")
    guard = 0
    while cur and cur in by_id and guard < 20000:
        chain.append(by_id[cur])
        cur = by_id[cur].get("parent")
        guard += 1
    chain.reverse()
    if len(chain) < idx * 0.5:
        chain = msgs[:idx]
    return chain


def ctx(chain, think_mode):
    last_user = -1
    for i, m in enumerate(chain):
        if m["role"] == "user":
            last_user = i
    total = 0
    for i, m in enumerate(chain):
        total += m["in_tok"]
        if think_mode == "all":
            total += m["think_tok"]
        elif think_mode == "turn" and i > last_user:
            total += m["think_tok"]
    return total


def evaluate(think_mode):
    e, a = [], []
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
        v = ctx(chain_for(msgs, idx), think_mode)
        e.append(v)
        a.append(s["prompt_tokens"])
    e = np.array(e, float)
    a = np.array(a, float)
    best = None
    for oh in range(0, 80001, 1000):
        for sc in [x / 100 for x in range(70, 261, 2)]:
            r = np.median(np.abs(oh + sc * e - a) / a)
            if best is None or r < best[0]:
                best = (r, oh, sc)
    _, oh, sc = best
    rel = np.abs(oh + sc * e - a) / a
    print(f"think={think_mode:5} n={len(e)} overhead={oh:>6,} scale={sc:.2f} "
          f"| rel err p50={np.percentile(rel,50):.1%} "
          f"p75={np.percentile(rel,75):.1%} p90={np.percentile(rel,90):.1%}")
    return oh, sc


for mode in ("none", "turn", "all"):
    evaluate(mode)

# Direct measurement of fixed overhead: trivial sessions (one user
# message, one assistant reply, no tools).
print("\n=== fixed overhead from trivial sessions ===")
triv = []
for s in sessions:
    msgs = s["msgs"]
    if len(msgs) != 2 or s["prompt_tokens"] <= 0:
        continue
    body = sum(m["in_tok"] + m["think_tok"] for m in msgs)
    if body > 400:
        continue
    triv.append((s["prompt_tokens"] - msgs[0]["in_tok"], s["created"],
                 s["title"][:30], body))
triv.sort(key=lambda t: t[1])
for t in triv:
    import datetime
    d = datetime.datetime.fromtimestamp(t[1]).strftime("%Y-%m-%d")
    print(f"  {d}  overhead~{t[0]:>7,}  body={t[3]:>4}  {t[2]}")
