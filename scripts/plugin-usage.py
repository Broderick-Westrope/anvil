#!/usr/bin/env python3
"""Report how often each skill, command and agent in an Anvil plugin is used.

Reads the Anvil database read-only and counts, per item, the distinct
top-level sessions that used it (subagent sessions roll up to their root).
Items are listed least used first, so never-used items come at the top.

What counts as a use:
  skill    the agent loaded it with view(skill_name=...), the user invoked it
           (a <skill_content> block in a user message), or the agent viewed
           its SKILL.md under the plugin directory. Path loads from sessions
           working inside the plugin repo are ignored as maintenance.
  command  a <command_expansion command="/name"> block in a user message.
           Before expansions were recorded, a command's body was pasted
           straight into the user message; those are matched against the
           opening of every historical version of the command in the plugin's
           git history.
  agent    a task tool call with that subagent_type.
Failed tool calls are not counted.

Usage:
  scripts/plugin-usage.py ~/dev/my-plugin
  scripts/plugin-usage.py ~/dev/my-plugin --recent-days 30 --json
"""

import argparse
import json
import os
import re
import sqlite3
import subprocess
from collections import defaultdict
from datetime import datetime, timezone

DEFAULT_DB = os.path.expanduser("~/.local/share/anvil/anvil.db")
KINDS = ("skill", "command", "agent")
SKILL_CONTENT = re.compile(r'<skill_content name="([^"]+)">')
COMMAND_EXPANSION = re.compile(r'<command_expansion command="/([^\s"]+)')
SKILL_PATH = re.compile(r"/skills/([^/]+)/SKILL\.md$")
HISTORICAL_COMMAND = re.compile(r"(?:^|/)commands/([^/]+?)(?:/COMMAND)?\.md$")
MIN_FINGERPRINT = 15
MAX_FINGERPRINT = 200


def load_items(plugin_dir):
    manifest = {}
    path = os.path.join(plugin_dir, "anvil-plugin.json")
    if os.path.isfile(path):
        with open(path) as f:
            manifest = json.load(f)

    def subdir(key):
        return os.path.join(plugin_dir, manifest.get(key, key))

    def entries(directory):
        return sorted(os.listdir(directory)) if os.path.isdir(directory) else []

    skills_dir, commands_dir, agents_dir = subdir("skills"), subdir("commands"), subdir("agents")
    return {
        "skill": [n for n in entries(skills_dir) if os.path.isfile(os.path.join(skills_dir, n, "SKILL.md"))],
        "command": sorted(
            {n for n in entries(commands_dir) if os.path.isfile(os.path.join(commands_dir, n, "COMMAND.md"))}
            | {n[:-3] for n in entries(commands_dir) if n.endswith(".md")}
        ),
        "agent": [n[:-3] for n in entries(agents_dir) if n.endswith(".md")],
    }


def git(repo, *args):
    result = subprocess.run(["git", "-C", repo, *args], capture_output=True, text=True)
    return result.stdout if result.returncode == 0 else ""


def worktree_paths(plugin_dir):
    paths = {plugin_dir}
    for line in git(plugin_dir, "worktree", "list", "--porcelain").splitlines():
        if line.startswith("worktree "):
            paths.add(os.path.realpath(line[len("worktree "):]))
    return paths


def body_fingerprint(text):
    """Return the opening of a command body, up to its first template variable."""
    lines = text.splitlines()
    if lines and lines[0].strip() == "---" and "---" in lines[1:]:
        lines = lines[lines.index("---", 1) + 1:]
    body = re.split(r"\$|\{\{", "\n".join(lines).strip())[0]
    return body[:MAX_FINGERPRINT] if len(body) >= MIN_FINGERPRINT else None


def canonical_command(name, commands):
    aliases = [c for c in commands if name == c or name.endswith((f"-{c}", f":{c}"))]
    return max(aliases, key=len, default=name)


def command_fingerprints(plugin_dir, commands):
    """Fingerprint every version of each command found anywhere in git history.

    Fingerprints shared by more than one command (such as a common delegation
    preamble) are dropped, since a match could not say which command ran.
    Commands that no longer exist are fingerprinted too, so their bodies are
    never mistaken for a current command. A historical name that only adds a
    namespace prefix to a current command (ce-grill for grill) is treated as
    the same command.
    """
    paths = defaultdict(set)
    for path in git(plugin_dir, "log", "--all", "--format=", "--name-only").splitlines():
        match = HISTORICAL_COMMAND.search(path)
        if match:
            paths[canonical_command(match.group(1), commands)].add(path)

    fingerprints = defaultdict(set)
    for name, name_paths in paths.items():
        for path in name_paths:
            for sha in git(plugin_dir, "log", "--all", "--format=%H", "--", path).split():
                fingerprint = body_fingerprint(git(plugin_dir, "show", f"{sha}:{path}"))
                if fingerprint:
                    fingerprints[name].add(fingerprint)

    owners = defaultdict(set)
    for name, prints in fingerprints.items():
        for fingerprint in prints:
            owners[fingerprint].add(name)
    return {
        name: {p for p in fingerprints[name] if len(owners[p]) == 1}
        for name in commands
        if name in fingerprints
    }


def parse_input(raw):
    try:
        value = json.loads(raw or "{}")
    except ValueError:
        return {}
    return value if isinstance(value, dict) else {}


def collect(db, plugin_dir, items):
    con = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
    sessions = {
        sid: (parent, working_dir or "")
        for sid, parent, working_dir in con.execute("SELECT id, parent_session_id, working_dir FROM sessions")
    }

    def root(sid):
        seen = set()
        while sessions.get(sid, (None, ""))[0] and sid not in seen:
            seen.add(sid)
            sid = sessions[sid][0]
        return sid

    maintenance_dirs = worktree_paths(plugin_dir)

    def is_maintenance(sid):
        working_dir = os.path.realpath(sessions.get(root(sid), (None, ""))[1] or "/")
        return any(working_dir == d or working_dir.startswith(d + os.sep) for d in maintenance_dirs)

    calls, failed, user_texts, latest = [], set(), [], 0
    for sid, role, parts, ts in con.execute("SELECT session_id, role, parts, created_at FROM messages"):
        latest = max(latest, ts)
        try:
            parts = json.loads(parts)
        except ValueError:
            continue
        for part in parts:
            kind, data = part.get("type"), part.get("data") or {}
            if kind == "tool_call":
                calls.append((data.get("id"), data.get("name"), parse_input(data.get("input")), sid, ts))
            elif kind == "tool_result" and data.get("is_error"):
                failed.add(data.get("tool_call_id"))
            elif kind == "text" and role == "user":
                user_texts.append((sid, ts, data.get("text") or ""))

    names = {kind: set(items[kind]) for kind in KINDS}
    plugin_marker = os.sep + os.path.basename(plugin_dir) + os.sep
    usage = {kind: defaultdict(lambda: defaultdict(list)) for kind in KINDS}

    for call_id, tool, args, sid, ts in calls:
        if call_id in failed:
            continue
        if tool == "view":
            if args.get("skill_name") in names["skill"]:
                usage["skill"][args["skill_name"]]["agent_load"].append((sid, ts))
            file_path = args.get("file_path") or ""
            match = SKILL_PATH.search(file_path)
            if (
                match
                and match.group(1) in names["skill"]
                and plugin_marker in file_path
                and not is_maintenance(sid)
            ):
                usage["skill"][match.group(1)]["path_load"].append((sid, ts))
        elif tool == "task" and args.get("subagent_type") in names["agent"]:
            usage["agent"][args["subagent_type"]]["task"].append((sid, ts))

    fingerprints = command_fingerprints(plugin_dir, items["command"])
    for sid, ts, text in user_texts:
        for name in SKILL_CONTENT.findall(text):
            if name in names["skill"]:
                usage["skill"][name]["user_invoke"].append((sid, ts))
        expanded = COMMAND_EXPANSION.findall(text)
        for name in expanded:
            if name in names["command"]:
                usage["command"][name]["user_invoke"].append((sid, ts))
        if expanded or "<command_expansion" in text or sessions.get(sid, (None, ""))[0]:
            continue
        stripped = text.lstrip()
        for name, prints in fingerprints.items():
            if any(stripped.startswith(p) for p in prints):
                usage["command"][name]["pasted_body"].append((sid, ts))
                break

    return usage, root, latest


def summarise(items, usage, root, recent_cutoff):
    def date(ts):
        return datetime.fromtimestamp(ts, timezone.utc).strftime("%Y-%m-%d") if ts else "-"

    rows = []
    for kind in KINDS:
        for name in items[kind]:
            signals = usage[kind].get(name, {})
            uses = [u for hits in signals.values() for u in hits]
            rows.append({
                "kind": kind,
                "name": name,
                "sessions": len({root(sid) for sid, _ in uses}),
                "recent_sessions": len({root(sid) for sid, ts in uses if ts >= recent_cutoff}),
                "uses": len(uses),
                "first": date(min((ts for _, ts in uses), default=None)),
                "last": date(max((ts for _, ts in uses), default=None)),
                "signals": {signal: len(hits) for signal, hits in sorted(signals.items())},
            })
    rows.sort(key=lambda r: (KINDS.index(r["kind"]), r["sessions"], r["uses"], r["name"]))
    return rows


def print_table(rows, recent_days):
    for kind in KINDS:
        kind_rows = [r for r in rows if r["kind"] == kind]
        if not kind_rows:
            continue
        unused = sum(1 for r in kind_rows if r["sessions"] == 0)
        print(f"\n{kind}s ({len(kind_rows)}, {unused} never used)")
        print(f"  {'name':34} {'sessions':>8} {f'last {recent_days}d':>8} {'uses':>6}  {'first':10}  {'last':10}  signals")
        for r in kind_rows:
            signals = ", ".join(f"{k}={v}" for k, v in r["signals"].items())
            print(
                f"  {r['name']:34} {r['sessions']:>8} {r['recent_sessions']:>8} {r['uses']:>6}"
                f"  {r['first']:10}  {r['last']:10}  {signals}"
            )


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("plugin", help="plugin directory (contains anvil-plugin.json)")
    parser.add_argument("--db", default=DEFAULT_DB)
    parser.add_argument("--recent-days", type=int, default=60, help="window for the recent sessions column")
    parser.add_argument("--json", action="store_true", help="print rows as JSON instead of a table")
    args = parser.parse_args()

    plugin_dir = os.path.realpath(os.path.expanduser(args.plugin))
    items = load_items(plugin_dir)
    if not any(items.values()):
        parser.error(f"no skills, commands or agents found in {plugin_dir}")

    usage, root, latest = collect(args.db, plugin_dir, items)
    rows = summarise(items, usage, root, latest - args.recent_days * 86400)
    if args.json:
        print(json.dumps(rows, indent=1))
    else:
        print_table(rows, args.recent_days)


if __name__ == "__main__":
    main()
