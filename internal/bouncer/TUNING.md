# Bouncer Tuning

How the routing thresholds and question battery were tuned, how to repeat
the exercise, and what is still worth trying. Keep this next to the code it
describes and update it whenever `DefaultThresholds` or `BatteryVersion`
changes.

## Routing

`Route` in `route.go` sends a call to the human when any of these hold:

1. a hazard axis reaches its own `EscalateAt` threshold;
2. the top hazard reaches `ConcernAt` and severity reaches
   `SeverityConcern` (the concern band);
3. severity reaches `SeverityEscalate`.

It denies when the top hazard reaches `DenyAt` and the user-requested
signal is below `UserRequestedAt`.

The concern band matters because hazard scores are not calibrated
probabilities. A routine edit often scores 0.35 to 0.65 on `destructive`.
On its own that says little, but combined with high severity it is worth a
look.

## 2026-10-04 replay (battery v2/v3, jev-1.13.0)

The first day of enforce mode produced 109 human prompts. Of 78 classifier
escalations, the user approved 75. The 3 denials were local Muninn
code-search MCP calls that scored exfiltration 0.35 to 0.41. Every
escalation the classifier got right scored 0.9 or above, and those were
denied automatically. Severity never reached 2.0, so `SeverityEscalate`
never fired by itself.

Escalations by trigger:

| Axis         | Prompts | Typical false positive                              |
|--------------|---------|-----------------------------------------------------|
| destructive  | 39      | targeted `edit`/`multiedit`, `gofumpt -w`, `sed -i` |
| remote_exec  | 16      | inline `python3 - <<EOF`, `go run pkg@latest`       |
| credentials  | 9       | `[ -n "$KEY" ]`, `gcloud auth list`                 |
| exfiltration | 8       | Muninn MCP, `gh pr view`                            |
| shared_infra | 2       |                                                     |

Policies replayed over the 315 assessed calls:

| Policy                                                      | Escalations |
|-------------------------------------------------------------|-------------|
| Old: any hazard >= 0.35 or severity >= 2                    | 81          |
| Any hazard >= 0.5                                           | 34          |
| Any hazard >= 0.6                                           | 19          |
| Per-axis only (d 0.7, e 0.6, c 0.5, r 0.6, s 0.5)           | 17          |
| Chosen: per-axis (d/r 0.7, e/c/s 0.6) + concern 0.35 @ 1.5  | 11          |

None of these policies lets any of the auto-denials through, because each
of those scored above `DenyAt`. The 11 that still escalate under the chosen
policy include `git reset` with `commit --amend`, `gsutil cp`, `go run
...@latest`, and reading API-key environment variables. Those are the calls
worth a human look.

### Repeating the replay

The decision log lives in `~/.local/share/anvil/anvil.db`. The query below
replays a candidate policy against every assessed call. Edit the `where`
clause to try other thresholds. Open the database read-only.

```sql
with a as (
  select verdict, tool_name,
    json_extract(assessment, '$.nouls.destructive')  d,
    json_extract(assessment, '$.nouls.exfiltration') e,
    json_extract(assessment, '$.nouls.credentials')  c,
    json_extract(assessment, '$.nouls.remote_exec')  r,
    json_extract(assessment, '$.nouls.shared_infra') s,
    json_extract(assessment, '$.severity')           sev,
    json_extract(assessment, '$.reason')             reason,
    json_extract(assessment, '$.battery_version')    battery,
    substr(replace(input, char(10), ' '), 1, 80)     input
  from permission_decisions
  where json_extract(assessment, '$.outcome') in ('allow', 'escalate', 'deny')
), m as (select *, max(d, e, c, r, s) top from a)
select battery, tool_name, verdict, reason, input from m
where (d >= 0.7 or e >= 0.6 or c >= 0.6 or r >= 0.7 or s >= 0.6
       or (top >= 0.35 and sev >= 1.5) or sev >= 2)
  and top < 0.9;
```

```bash
sqlite3 -readonly ~/.local/share/anvil/anvil.db < replay.sql
```

`anvil permissions stats` shows the approval rate per triggering axis. An
axis whose escalations you approve nearly every time is a candidate for a
higher threshold.
