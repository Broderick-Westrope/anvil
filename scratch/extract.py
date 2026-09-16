"""Extract orchestrator turns from anvil.db with token estimates.

Writes scratch/turns.json for downstream percentile analysis.
"""

import collections
import json
import sqlite3
import sys

DB = "/Users/broderick.westrope/.local/share/anvil/anvil.db"


def tok(s):
    return (len(s) + 3) // 4 if s else 0


def part_tokens(p):
    t = p.get("type")
    d = p.get("data") or {}
    if t == "text":
        return tok(d.get("text", "")), 0
    if t == "reasoning":
        # Thinking tokens are billed as output but not resent as input
        # beyond the current turn; count separately.
        return 0, tok(d.get("thinking", ""))
    if t == "tool_call":
        return tok(d.get("name", "")) + tok(d.get("input", "")) + 8, 0
    if t == "tool_result":
        return tok(d.get("content", "")) + tok(d.get("data", "")) + 8, 0
    return 0, 0


def main():
    conn = sqlite3.connect(DB)
    conn.row_factory = sqlite3.Row

    roots = {
        r["id"]: dict(r)
        for r in conn.execute(
            "select id, title, cost, prompt_tokens, completion_tokens,"
            " created_at, working_dir from sessions"
            " where parent_session_id is null"
        )
    }

    child_cost = collections.defaultdict(float)
    child_rows = collections.defaultdict(list)
    for r in conn.execute(
        "select id, parent_session_id, cost, created_at, updated_at"
        " from sessions where parent_session_id is not null"
    ):
        child_cost[r["parent_session_id"]] += r["cost"]
        child_rows[r["parent_session_id"]].append(dict(r))

    by_session = collections.defaultdict(list)
    n = 0
    for r in conn.execute(
        "select id, session_id, role, message_type, model, provider,"
        " created_at, finished_at, parent_message_id, parts from messages"
    ):
        if r["session_id"] not in roots:
            continue
        text_t, think_t = 0, 0
        finish = None
        tool_names = []
        try:
            parts = json.loads(r["parts"])
        except Exception:
            parts = []
        for p in parts:
            a, b = part_tokens(p)
            text_t += a
            think_t += b
            if p.get("type") == "finish":
                finish = (p.get("data") or {}).get("reason")
            elif p.get("type") == "tool_call":
                tool_names.append((p.get("data") or {}).get("name", ""))
        by_session[r["session_id"]].append(
            {
                "id": r["id"],
                "role": r["role"] or "",
                "mtype": r["message_type"],
                "model": r["model"],
                "provider": r["provider"],
                "created": r["created_at"],
                "finished": r["finished_at"],
                "parent": r["parent_message_id"],
                "in_tok": text_t,
                "think_tok": think_t,
                "finish": finish,
                "tools": tool_names,
            }
        )
        n += 1
    print(f"loaded {n} messages across {len(by_session)} root sessions",
          file=sys.stderr)

    out = {"sessions": [], "child_cost": child_cost, "child_rows": child_rows}
    for sid, msgs in by_session.items():
        msgs.sort(key=lambda m: (m["created"], m["id"]))
        out["sessions"].append(
            {
                "id": sid,
                "cost": roots[sid]["cost"],
                "child_cost": child_cost.get(sid, 0.0),
                "prompt_tokens": roots[sid]["prompt_tokens"],
                "completion_tokens": roots[sid]["completion_tokens"],
                "created": roots[sid]["created_at"],
                "title": roots[sid]["title"],
                "working_dir": roots[sid]["working_dir"],
                "msgs": msgs,
            }
        )

    with open("scratch/turns.json", "w") as f:
        json.dump(out["sessions"], f)
    print("wrote scratch/turns.json", file=sys.stderr)


main()
