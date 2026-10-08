> **Parked (October 2026).** Extending Anvil is both more enjoyable and more likely to improve the workflow than a hosted agent. A hosted setup would also mean rebuilding local context in a sandbox: auth tokens and keys, repo checkouts, MCP servers, code search. Revisit only if there's a recurring need for agents to keep running while the laptop is closed.

# Personal Cloud Coding-Agent Setup — Handover Summary

*Conversation date: 27 September 2026. Prices and product details were checked against web sources on that date. Several products are in beta or preview, so re-verify before committing.*

## 1. Goal and context

- Take a local, personal coding-agent setup to the cloud, **independent of the company's parallel effort**, to avoid being tied to its timelines or decisions.
- Requirements:
  - serverless / scale-to-zero, because agents aren't always running;
  - highly variable concurrency;
  - the ability to scale different agent types independently.
- The user already has **their own local agent harness** and enjoys iterating on it. Keeping that harness is a strong preference.
- Constraint noted: this workspace's admin policy limits hosting recommendations to **GCP and Cloudflare**. Azure was compared on price only, because the user asked about it.
- Hygiene: keep company repos, credentials and data out of the personal setup.

## 2. Vendor pricing comparison

| | Cloudflare Containers / Sandboxes | Azure Container Apps (Consumption) | GCP Cloud Run |
|---|---|---|---|
| CPU | $0.000020 per vCPU-s, **active CPU only** | $0.000024 per vCPU-s, **allocated** | $0.000024 per vCPU-s (Tier 1) |
| Memory | $0.0000025 per GiB-s, on the provisioned tier while awake | $0.000003 per GiB-s | $0.0000025 per GiB-s |
| Free allowance | 375 vCPU-min, 25 GiB-h memory, 200 GB-h disk | 180k vCPU-s, 360k GiB-s, 2M requests | Same as Azure (larger with instance-based billing: 240k vCPU-s, 450k GiB-s) |
| Base fee | $5/month (Workers Paid) | None | None |
| Scaling model | Programmatic, one instance per ID | Native autoscaling per app | Native autoscaling per service |
| Sizing | Fixed tiers (1/16 vCPU / 256 MiB up to 4 vCPU / 12 GiB) | Flexible | Flexible |

**Key insight:** coding agents are mostly I/O-bound, waiting on LLM calls. Cloudflare's active-CPU billing favours that pattern. Azure and Cloud Run bill allocated CPU whether or not it's used. The crossover is roughly 60% CPU-active; above that, allocation-based platforms become competitive.

> **Correction (later review):** the 60% crossover is wrong. At these rates Cloudflare is cheaper per second at any CPU share, because its memory rate is also lower. It loses only on the $5 base fee and the smaller free allowance, so the crossover depends on volume. At the example below, Cloudflare ≈ $9.73 + $10.80 × CPU share, which draws level with Azure at ~40% and Cloud Run at ~30%. At personal scale the platforms are $2–3/month apart, so price shouldn't decide.

**Example** (300 sessions × 30 min at 1 vCPU / 4 GiB, 10% CPU-active): Cloudflare ≈ $11, Azure ≈ $14, Cloud Run ≈ $13 per month.

**Decision:** explore Cloudflare. It fits idle-heavy agent workloads and per-session isolation well. Cloud Run is the fallback if conventional per-service autoscaling is preferred.

## 3. Proposed Cloudflare architecture

| Layer | Product | Role |
|---|---|---|
| Front door | Worker + Cloudflare Access | Receives tasks from the CLI, phone, GitHub webhooks and cron; Access restricts it to the user. |
| Brain | Agents SDK (Durable Object per task) | Holds the plan, history and status in SQLite; streams progress over WebSocket; hibernates when idle. |
| Hands | Sandbox SDK (container per task) | Runs the harness or agent. **One sandbox class per agent type**, each with its own image and instance size (e.g. a small tier for reviewers, standard-3 at 2 vCPU / 8 GiB for builders). |
| Multi-step jobs | Workflows / `AgentWorkflow` | Plan → implement → test → PR, with durable retries and human approval gates. |
| Storage | R2 | Workspace snapshots (`createBackup` / `restoreBackup`), logs, artifacts. |
| | D1 | Index of all tasks, their type and status. |
| | KV | Backup handles, config. |
| LLM traffic | AI Gateway | Per-agent cost tracking, rate limits, caching, logs. |
| Secrets | Sandbox outbound interception | The Worker injects auth headers, so API keys and GitHub tokens never enter the container. |

**Flow:** task → Worker → task agent → Workflow → sandbox restores snapshot → agent works → pushes branch / opens PR → snapshot → sandbox parked → agent notifies the user.

**Watch-outs:**
- Project Think is in preview and the Agents SDK is pre-1.0, so pin versions.
- Container cold starts take a few seconds.
- Memory bills for the whole time a container is awake.

## 4. Cost breakdown

Assumptions: 30-minute tasks on standard-3 (2 vCPU / 8 GiB), 10% CPU-active.

| | Light (100 tasks) | Medium (500) | Heavy (2,000) |
|---|---|---|---|
| Workers Paid base | $5.00 | $5.00 | $5.00 |
| Sandbox memory | $3.40 | $17.80 | $71.80 |
| Sandbox CPU | $0.30 | $3.20 | $14.00 |
| Sandbox disk | $0.15 | $1.00 | $4.00 |
| Everything else | ~$0 | ~$0–1 | ~$2–5 |
| **Total / month** | **~$9** | **~$27** | **~$97** |

Notes on the "everything else" line:
- **Workers:** 10M requests/month included, no egress fees.
- **Durable Objects:** 1M requests and 400k GB-s included; overage rounds up to the next billable unit.
- **Workflows:** step billing started 10 Aug 2026. ~10 steps per task should stay within the allowance, but check the dashboard.
- **R2:** $0.015/GB-month with 10 GB free; snapshots default to a 3-day TTL.
- **D1 / KV:** effectively free at this scale.
- **AI Gateway:** core features free; 1M logs included on Paid.
- **Access:** free up to 50 users.

**Cost levers:**
- A shorter `sleepAfter`: each idle minute bills full memory, and 5 minutes adds about 15%.
- Right-sizing agent types onto smaller tiers.
- The WebSocket Hibernation API in task agents.

**Tokens dominate:** at an illustrative $1 per task, the Medium tier is about $500 in tokens against about $27 in infrastructure. AI Gateway visibility matters more than infrastructure tuning.

## 5. Winding down idle sandboxes

**Principle:** the container is disposable; state lives in git (a WIP branch), an R2 snapshot, and the task agent's SQLite.

**Lifecycle:**
1. **Active:** the container runs the work.
2. **Checkpoint** at natural pauses (turn done, awaiting approval, awaiting CI): push a WIP commit, `createBackup({ gitignore: true })`, store the handle in the Durable Object.
3. **Destroy:** use `destroy()` rather than sleep, because sleeping containers still count toward account limits.
4. **Cold:** only the Durable Object and R2 remain, costing near $0.
5. **Resume:** an event wakes the agent, which starts a fresh container and calls `restoreBackup()`.

**Important gotchas with `sleepAfter`:**
- Background processes inside the container **don't** reset the idle timer. Only `renewActivityTimeout()`, called from the Worker or Durable Object, does. Long, quiet jobs can be killed.
- In the 0.13 SDK line, the timer renews if any process or terminal is active. One team found 25 sandboxes awake 10–16 hours past a 5-minute `sleepAfter`. **Fix:** the Durable Object owns the idle deadline: record last activity, sweep every 60 seconds, destroy when idle. Keep `sleepAfter` as a backstop only.
- `keepAlive: true` disables sleeping entirely and requires an explicit `destroy()`.

**Other caveats:**
- The restored mount is lost on sleep or restart, so re-restore on every wake.
- Stop the agent's processes before snapshotting; partially written files may not be captured consistently.
- Extend the backup TTL beyond 3 days for tasks that may sit parked over a weekend.
- Keep a per-repo "base" snapshot with dependencies installed for faster resume.

**Bigger lever:** run the agent's reasoning loop in the Durable Object and use the sandbox only for tool calls. The sandbox then sleeps during LLM calls. The trade-off is more cold starts; park only on gaps longer than a few minutes.

## 6. Implementation difficulty

| Level | What | Effort | Recommendation |
|---|---|---|---|
| 1 | Short `sleepAfter` + `destroy()` at task end | A few lines | Do from day one |
| 2 | Checkpoint-and-park at pause points + Durable Object idle sweep | A few extra days, mostly edge cases | Do from day one |
| 3 | Agent loop outside the container | Significant: you rebuild the harness (tool loop, file editing, context management, recovery) | Defer until bills justify it; infrastructure is tens of $/month against hundreds in tokens |

## 7. Off-the-shelf alternatives

| Option | Monthly cost at Medium (excl. tokens) | Keeps your harness? | Notes |
|---|---|---|---|
| Own Cloudflare build | ~$27 | **Yes** | Full control |
| Claude Managed Agents (beta) | ~$20 (250 session-hours × $0.08) | No | Tokens + $0.08 per active session-hour; idle is free. Sandboxing, vaults and tracing included. Self-hosted sandbox option, but the loop stays on Anthropic's side. |
| Claude Code on the web | $0 extra, but draws on plan limits | No | Included in Pro, Max and Team; managed VMs; "routines" for scheduled, HTTP-triggered or GitHub-event runs; task-scoped (idle sessions reclaimed). |

**Evaluation:** price is roughly equal and tokens dominate every option. Build if the harness is the interesting part, you want custom agent types or orchestration, or you value the learning. Otherwise, hosted options cover "fire a task, get a PR."

## 8. Recommended next steps

1. **Now:** use Claude Code on the web for PR-shaped tasks, for immediate cloud agents independent of the company's timeline.
2. **In parallel:** scaffold the Cloudflare project (`wrangler.jsonc`, a task agent, one sandbox class) around the existing harness, with Levels 1–2 lifecycle management built in.
3. Add AI Gateway early for per-agent token cost visibility.
4. Benchmark the harness against Claude Code on the web on real tasks.
5. Revisit Level 3 only if the sandbox memory line becomes material.

## Key sources

- Cloudflare Containers pricing: https://developers.cloudflare.com/containers/pricing/
- Sandbox SDK lifecycle: https://developers.cloudflare.com/sandbox/concepts/sandboxes/
- Sandbox options (`sleepAfter`, `keepAlive`): https://developers.cloudflare.com/sandbox/configuration/sandbox-options/
- Container class (`onActivityExpired`, `renewActivityTimeout`): https://developers.cloudflare.com/containers/container-class/
- `sleepAfter` leak incident and Durable Object-owned deadline fix: https://github.com/coreplanelabs/switchboard/pull/1355
- Claude Managed Agents overview: https://platform.claude.com/docs/en/managed-agents/overview
- Claude plans and pricing: https://claude.com/pricing
