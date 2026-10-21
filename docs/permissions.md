# Permissions

By default, Anvil asks you for permission before running tool calls. You can
define rules that automatically allow, ask, or deny instead.

Rules are keyed by a glob matching the tool name, and the value is either an
action (`allow`, `ask`, `deny`) or an object of per-input rules. Rules are
evaluated **in order, and the last match wins** — so put broad rules first and
narrow exceptions after.

```json
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json",
  "permissions": {
    "view": "allow",
    "ls": "allow",
    "grep": "allow",
    "{edit,write}": "allow",
    "mcp_context7_*": "allow",
    "bash": {
      "*": "ask",
      "git status *": "allow",
      "git diff *": "allow",
      "go test *": "allow",
      "rm *": "deny"
    }
  }
}
```

The input a rule matches against depends on the tool: the full command for
`bash`, the file path for `edit`/`write`/`view`/`ls`, the URL for
`fetch`/`download`. MCP tools match on tool name only.

Patterns support `*` (any characters, including `/`), `?` (one character),
`[abc]` (character class), and `{a,b}` (brace expansion). A trailing `" *"`
also matches the bare command, so `git status *` covers plain `git status`.

## Chained Commands

Bash commands are split into their individual commands before evaluation, and
**every** part must be allowed for the command to run without prompting:

```
go test ./... 2>&1 | tail -20    needs "go test *" and "tail *"
git log --oneline && rm -rf /    denied, because of the "rm *" deny rule
```

This means rules compose, and a dangerous command can't ride along with an
allowed one.

Three things become segments of their own, because each executes or writes
something the command name alone doesn't reveal:

```
echo hi > ~/.zshrc               needs "echo *" and "> ~/.zshrc"
env FOO=bar rm -rf /             needs "env *" and "rm -rf /"
find . -exec rm {} \;            needs "find *" and "rm {}"
```

Redirections that can write to a file (`>`, `>>`, `>|`, `&>`, and `>&` to a
path) split off as a segment like `> /tmp/out.txt`, so allowing `echo *`
never also grants a write to an arbitrary path. Redirections that cannot
write a file stay part of the command: fd duplication such as `2>&1`,
heredocs, and `<` reads.

Commands that run another command given in their arguments — `env`, `sudo`,
`xargs`, `timeout`, `nice`, `nohup`, `command`, `exec` and friends — also
contribute the inner command as a segment, as does the body of a
`find -exec` or `-ok` clause. Allowing the wrapper doesn't implicitly allow
everything it can launch. Shell code passed as a string to `sh -c`,
`bash -c`, `eval`, or `env -S` is split and evaluated too.

Each command is also matched in a normalised spelling, with quotes and
escapes removed, braces expanded, and paths reduced to the command name,
so a deny rule can't be dodged by respelling the command:

```
'rm' -rf x    r''m -rf x    \rm -rf x    /bin/rm -rf x    {rm,-rf} x
```

All of these are denied by `"rm *": "deny"`. Command names only known at
runtime (`$cmd`, `$(echo rm)`) can't be resolved, so they always prompt.

Because segments combine worst-outcome-first, splitting these out can only
make a command stricter, never more permissive. The one cost is that a
quoted command name such as `"git" status` prompts even when `git status *`
is allowed.

## Granting at the Prompt

When Anvil asks for permission you can allow it once, for the session, or
forever. Session and forever grants use the editable pattern shown in the
dialog (press `e` to edit it), so you can widen `git push origin main` to
`git push *` before granting. Redirection segments are never widened, so
approving a write to one path doesn't grant writes everywhere. Forever grants
are written to your config — either the project config (`.anvil/anvil.json`)
or your user config.

## Yolo Mode

Running with `--yolo` turns every `ask` into `allow` while still honouring
`deny` rules. `--yolo=full` bypasses permissions entirely, including `deny`
rules and the [bouncer](#bouncer). Be very, very careful with these.

`ctrl+y` cycles yolo off, standard, and full while Anvil is running. The
editor gutter shows the current level: an amber ` ! ` for standard and a red
`!!!` for full.

To start every session at a level without passing the flag, set `yolo` in
your **user-level** config (`~/.config/anvil/anvil.json`). Like the bouncer,
it is ignored in project and workspace configs, so a cloned repo can't switch
it on.

```json
{
  "yolo": "standard"
}
```

Values are `off` (the default), `standard` and `full`. `--yolo`,
`--yolo=full` and `--yolo=false` override it for one run, and `anvil run`
ignores it.

## Bouncer

The bouncer is an optional classifier that answers permission prompts on
your behalf. Like a bouncer at a bar, it lets a call in, turns it away, or
checks its ID by sending it to you. It only sees calls that no `allow` or
`deny` rule resolves, so explicit rules always win.

```
Tool call
  explicit allow or deny rule   ->  apply the rule
  no rule (or an ask rule)      ->  bouncer
      allow                     ->  run
      deny                      ->  block, and stop the agent's turn
      unsure, error, or skipped ->  prompt you (or approve, under --yolo)
```

It talks to any TypeSafe-compatible System One endpoint, such as
[Jev](https://docs.typesafe.ai). Each check costs about 1,100 input tokens
and 250ms. Configure it in your **user-level** config only
(`~/.config/anvil/anvil.json`). A `bouncer` block in a project or workspace
config is ignored, so a cloned repo can't switch it on or loosen it.

```json
{
  "bouncer": {
    "mode": "enforce",
    "url": "https://api.typesafe.ai/v1/systemone",
    "model": "jev-1.13.0",
    "auth_scheme": "Bearer",
    "api_key_env": "TYPESAFE_API_KEY"
  }
}
```

- `mode`: `off` (the default) makes no calls. `shadow` asks the bouncer but
  still prompts you, logging its verdict alongside your decision. `enforce`
  acts on its verdict.
- `api_key_env`: the environment variable holding the key (default
  `BASETEN_API_KEY`). It's read once at startup, before any project `env`
  is applied.
- `send_user_messages`: whether your last three messages are sent as
  context (default `true`). The bouncer uses them to tell an action you
  asked for from one you didn't.
- Pin a versioned `model` so a new release can't silently change verdicts.
- `timeout_seconds` and the thresholds below are documented in the schema.

The bouncer scores each call on five hazard axes (`destructive`,
`exfiltration`, `credentials`, `remote_exec`, `shared_infra`) and on how
bad a mistake would be (`severity`, 0 to 3). A call comes to you when:
- any axis reaches its own `escalate_at` (0.5 for `destructive`, 0.6 for
  the rest);
- an axis reaches `concern_at` (0.35) and severity reaches
  `severity_concern` (1.5), so a borderline call only bothers you when
  getting it wrong would hurt; or
- severity alone reaches `severity_escalate` (2).

It's flagged as a deny when an axis reaches `deny_at` (0.9), severity
reaches `severity_deny` (2), and you didn't ask for it
(`user_requested_at`, 0.7). A deny isn't final: it comes to you as a red
prompt that starts on **Deny** and has no "allow forever" option. Allow it
if the bouncer got it wrong, or deny it, with an optional reason for the
agent. Your answer is logged next to the bouncer's verdict, so
`anvil permissions stats` shows how often its denials were wrong. In yolo
mode, denies are the only prompts you'll see. Setting `escalate_at`
applies one value to every axis; `escalate_at_axes` overrides individual
ones:

```json
{
  "bouncer": {
    "escalate_at_axes": { "credentials": 0.5, "destructive": 0.8 }
  }
}
```

Some calls always come to you without a classifier call:
- writes to protected paths (`.git/`, `anvil.json`, CI workflows, shell rc
  files, `~/.ssh`);
- reads of likely secrets;
- command names only known at runtime;
- MCP calls with no arguments, and oversized inputs;
- any call while the bouncer is unreachable.

When it does pass a call to you, the prompt shows its full verdict, with
the axes that triggered it highlighted.

Prompts the bouncer escalated or flagged as a deny also get a second
opinion from your small model, which reads your last five messages and
judges whether you asked for the call. It can only quote you: to say it
would allow a call, it has to give your exact words, and Anvil checks they
appear in your messages. It can soften a deny to an ordinary prompt but
never approve it, and it never approves a call with severity 2.5 or more.
For now it runs in shadow: its opinion is shown under the bouncer's as
`Reviewer (shadow)` and logged, but it never changes what happens.
`anvil permissions stats` compares it with your answers. Set
`"review": "off"` in the `bouncer` block to turn it off. `ctrl+q` cycles the mode while
Anvil is running, without changing your config. The editor gutter shows a
hollow ` ◇ ` in shadow and a solid ` ◆ ` in enforce, on the line above any
yolo badge. The bouncer is warmed up in the background whenever it's
switched on, so a slow cold start doesn't delay your first prompt.

> [!WARNING]
> Every call the bouncer assesses sends the command, diff, or MCP arguments
> (after best-effort secret redaction) and, by default, your recent
> messages to the configured endpoint. Only point it at a provider you're
> allowed to send that data to.

To check a model before trusting it, run the calibration harness against
its synthetic and real-world cases. It fails if any dangerous case is allowed:

```bash
ANVIL_BOUNCER_LIVE=1 \
ANVIL_BOUNCER_URL=https://api.typesafe.ai/v1/systemone \
ANVIL_BOUNCER_MODEL=jev-1.13.0 \
ANVIL_BOUNCER_AUTH_SCHEME=Bearer \
ANVIL_BOUNCER_API_KEY_ENV=TYPESAFE_API_KEY \
go test ./internal/bouncer -run TestLiveCalibration -v
```

## Turning Approvals into Rules

Every permission decision is logged with who made it: a rule, the bouncer,
yolo, or you. The log is kept for 90 days. Two commands read it:

```bash
# Propose explicit rules for calls you keep approving.
anvil permissions triage --days 14

# See how calls were decided, and how the bouncer is doing.
anvil permissions stats --days 30
```

`triage` groups repeated approvals into narrow patterns and lets you pick
which to write to your config:
- Tier A patterns are safe for any argument (`git status *`,
  `git rev-parse *`) and can be applied in bulk with `--yes`.
- Tier B patterns need you to review every argument they allow, so you
  pick them one at a time.
- Destructive, exec, network, and mutating commands are never proposed.
- Every proposal is checked against all logged decisions it would match
  and simulated before it's written.
- Deny rules are only proposed from denials you made yourself, so the
  bouncer's false positives never become permanent rules.

Anvil reminds you at startup once 50 undecided calls have built up in a
week. `stats` reports how often you approve the bouncer's escalations,
broken down by the axis that triggered them. An axis you approve nearly
every time is one the bouncer is too cautious about, and a good candidate
for an explicit allow rule.

> [!NOTE]
> The older `permissions.allowed_tools` list is deprecated. It still works
> (each entry becomes an `allow` rule) but logs a warning, and it cannot be
> combined with the rule format above.

