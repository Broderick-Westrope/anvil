Execute shell commands; long-running commands automatically move to background and return a shell ID.

<cross_platform>
Uses mvdan/sh interpreter (Bash-compatible on all platforms including Windows).
Use forward slashes for paths: "ls C:/foo/bar" not "ls C:\foo\bar".
Common shell builtins and core utils available on Windows.
</cross_platform>

<execution_steps>
1. Directory Verification: If creating directories/files, use LS tool to verify parent exists
2. Security Check: Banned commands ({{ .BannedCommands }}) return error - explain to user. Safe read-only commands execute without prompts
3. Command Execution: Execute with proper quoting, capture output
4. Auto-Background: Commands exceeding 1 minute (default) automatically move to background and return shell ID; for commands known to be slow whose result you need before continuing, raise the threshold up to 600 seconds via `auto_background_after`
5. Output Processing: Truncate if exceeds {{ .MaxOutputLength }} characters
6. Return Result: Include errors, metadata with <cwd></cwd> tags
</execution_steps>

<usage_notes>
- Command required, working_dir optional (defaults to current directory)
- IMPORTANT: Use Grep/Glob/Agent tools instead of 'find'/'grep'. Use View/LS tools instead of 'cat'/'head'/'tail'/'ls'
- Chain with ';' or '&&', avoid newlines except in quoted strings
- Each command runs in independent shell (no state persistence between calls)
- Prefer absolute paths over 'cd' (use 'cd' only if user explicitly requests)
{{- if .RgAvailable }}
- Ripgrep (`rg`) is available; prefer it over `grep` for faster, more intuitive searching
{{- end }}
</usage_notes>

<background_execution>
- Set run_in_background=true to run commands in a separate background shell
- Returns a shell ID for managing the background process
- Use job_output tool to view current output from background shell
- Use job_kill tool to terminate a background shell
- IMPORTANT: NEVER use `&` at the end of commands to run in background - use run_in_background parameter instead
- Before starting a long-lived server or tunnel, check job_list for an existing one you can reuse.
- Every agent with bash has job_output, job_kill, and job_list.
- For servers, start with run_in_background and use job_output with wait=true and pattern (e.g. "listening on|ready") instead of sleep loops.
- Commands that should run in background:
  * Long-running servers (e.g., `npm start`, `python -m http.server`, `node server.js`)
  * Watch/monitoring tasks (e.g., `npm run watch`, `tail -f logfile`, CI watchers per <ci_checks>)
  * Continuous processes that don't exit on their own
  * Any command expected to run indefinitely
- Commands that should NOT run in background:
  * Build commands (e.g., `npm run build`, `go build`)
  * Test suites (e.g., `npm test`, `pytest`)
  * Git operations
  * File operations
  * Short-lived scripts
</background_execution>

<ci_checks>
After pushing to a branch that has a pull request (including right after `gh pr create`), watch CI in the background:
- Start one watcher straight away with run_in_background=true:
  `for i in $(seq 1 30); do gh pr checks <pr> --json name --jq length 2>/dev/null | grep -q '^[1-9]' && break; sleep 10; done; gh pr checks <pr> --watch --fail-fast --interval 30`
  The loop waits for checks to register; right after a push `gh pr checks` fails with "no checks reported".
- Whenever you read the watcher with job_output, set tail_lines=20 or less. `--watch` reprints the whole checks table on every refresh, so everything before the last table is repeats.
- Keep working on anything else that remains while it runs. You are notified when it exits: exit 0 means every check passed, non-zero means a check failed or the watch broke.
{{- if .WakeOnJobEvents }}
- When nothing else is left, end your turn instead of waiting so the user can keep talking to you. Say CI is still running, give the job ID, and say you'll pick up the result when it finishes. Don't block on it with job_output wait=true unless the user asked you to wait for CI.
- The result reaches you either in a new turn started for you when the watcher exits while the session is idle, or alongside the user's next message. On a pass, report it in one line.
- On failure, take the run ID from the failing check's link and read only what you need: `gh run view <run-id> --log-failed | tail -n 100`. If the failure is within the task you were given, fix it and run the checks you can locally. If the result arrived in a turn started without a new user message, report the failure and your fix and ask before pushing again; never push repeatedly while the user is away. After any new push, start a new watcher.
{{- else }}
- When nothing else is left, block on it with job_output wait=true (raise timeout_seconds for slow pipelines). Don't report the work as done while its CI is still running unless the user said not to wait.
- On failure, take the run ID from the failing check's link and read only what you need: `gh run view <run-id> --log-failed | tail -n 100`. After pushing a fix, start a new watcher.
{{- end }}
- For pushes without a pull request (e.g. tags or main), get the run ID with `gh run list --branch <branch> --limit 1` and watch it the same way with `gh run watch <run-id> --exit-status --interval 30`.
- NEVER poll CI with `sleep N; gh pr checks ...` or your own status-polling loops; the single background watcher above replaces them.
- Skip this when the repository has no CI or the user said not to wait for it.
</ci_checks>

<git_commits>
When creating a git commit, whether asked to or committing as you go in a linked worktree:

1. Single message with three tool_use blocks (IMPORTANT for speed):
   - git status (untracked files) and git rev-parse --git-dir --git-common-dir (equal paths mean the root worktree)
   - git diff (staged/unstaged changes)
   - git log (recent commit message style)

2. Stage only the files relevant to this commit by explicit path (`git add <path>...`), including untracked ones. Don't commit files already modified at conversation start unless relevant. Never commit in the root worktree unless the user or a repository memory file explicitly says to; follow <git_workflow> and commit in a linked worktree instead.

3. Analyze staged changes in <commit_analysis> tags:
   - List changed/added files, summarize nature (feature/enhancement/bug fix/refactoring/test/docs)
   - Brainstorm purpose/motivation, assess project impact, check for sensitive info
   - Don't use tools beyond git context
   - Draft concise (1-2 sentences) message focusing on "why" not "what"
   - Use clear language, accurate reflection ("add"=new feature, "update"=enhancement, "fix"=bug fix)
   - Avoid generic messages, review draft

4. Create commit using HEREDOC:
   git commit -m "$(cat <<'EOF'
   Commit message here.
   EOF
   )"

5. If pre-commit hook fails, retry ONCE. If fails again, hook preventing commit. If succeeds but files modified, MUST amend.

6. Run git status to verify.

Notes: Never use "git commit -a", "git commit -am", "git add -A" or "git add .", don't stage unrelated files, NEVER update config, don't push, no -i flags, no empty commits, return empty response, when rebasing always use -m.
</git_commits>

<pull_requests>
Use gh command for ALL GitHub tasks. When user asks to create PR:

1. Single message with multiple tool_use blocks (VERY IMPORTANT for speed):
   - git status (untracked files)
   - git diff (staged/unstaged changes)
   - Check if branch tracks remote and is up to date
   - git log and 'git diff main...HEAD' (full commit history from main divergence)

2. If in the root worktree or on the default branch, move the work to a linked worktree on a feature branch as described in the `using-git-worktrees` skill
3. Commit changes if needed, following <git_commits>
4. Push to remote with -u flag if needed

5. Analyze changes in <pr_analysis> tags:
   - List commits since diverging from main
   - Summarize nature of changes
   - Brainstorm purpose/motivation
   - Assess project impact
   - Don't use tools beyond git context
   - Check for sensitive information
   - Draft concise (1-2 bullet points) PR summary focusing on "why"
   - Ensure summary reflects ALL changes since main divergence
   - Clear, concise language
   - Accurate reflection of changes and purpose
   - Avoid generic summaries
   - Review draft

6. Create PR with gh pr create using HEREDOC:
   gh pr create --title "title" --body "$(cat <<'EOF'

   ## Summary

   <1-3 bullet points>

   ## Test plan

   [Checklist of TODOs...]

   EOF
   )"

7. Start a background CI watcher for the new PR as described in <ci_checks>.

Important:

- Return empty response - user sees gh output
- Never update git config
</pull_requests>

<examples>
Good: pytest /foo/bar/tests
Bad: cd /foo/bar && pytest tests
</examples>
