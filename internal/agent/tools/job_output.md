Read output from a background job by ID; returns only new output since the previous call. Set wait=true to block until it completes, a pattern matches, or a timeout; set pattern with wait=false to be notified when a line matches.

<usage>
- Provide the job ID returned by bash or job_list
- Each call returns only output written since the previous job_output call
  on this job; the first call returns everything from the start, even
  after a command was auto-backgrounded with a preview of its output
- stdout is shown first, then stderr
- "(no new output)" means nothing new since the last read; "no output"
  means the job has not printed anything yet
</usage>

<parameters>
- full: return all output from the start instead of only new output; the
  next incremental call returns only output written after this one
- tail_lines: return only the last N lines of what this call would return,
  with "(K earlier lines omitted)"; the skipped lines count as read
- wait: block until the job completes, pattern matches, or timeout_seconds
  elapses; wait=false (the default) never blocks
- timeout_seconds: with wait=true, the maximum seconds to wait; defaults to
  300, values above 1800 are clamped to 1800
- pattern: RE2 regex matched against stdout and stderr line by line,
  including lines that arrived before this call but were not yet read;
  cannot be combined with full=true
  - with wait=true: returns as soon as a line matches, with all new output
  - with wait=false: returns new output immediately and sets a one-shot
    watch; you get a notification when a matching line appears (at once if
    an unread line already matches). Each job has one watch; a new pattern
    replaces the previous one. The watch ends when the job exits
</parameters>

<notifications>
- Every background job notifies you automatically when it completes, with
  its exit code and last lines; no polling is needed
- Watches notify you when their pattern matches
- Notifications arrive as a system reminder at your next step; you are not
  notified of anything a job_output or job_kill result already showed you
</notifications>

<status_header>
The first line reports the job state and runtime:
- Status: running (4m12s, last output 38s ago)
- Status: running (12s, no output yet)
- Status: completed, exit 1 (9m14s)
With wait=true, it also says why the wait ended:
- Status: running (5m00s), wait timed out after 300s
- Status: running (12s), matched "ready in 141 ms"
- Status: running (12s), wait canceled
- Status: completed, exit 0 (13s), matched "..."
Every read of a completed job includes its exit code, including empty and
full re-reads.
</status_header>

<persistence>
- Job IDs are unique across Anvil restarts and are never reused
- Output and exit codes are saved, so jobs stay readable after they drop
  out of memory and after Anvil restarts; saved output is kept for 14
  days, after which reads return "(output expired on <date>)"
- Jobs die when Anvil exits and are then reported as "Status: killed when
  Anvil exited", or as "Status: interrupted" if Anvil exited unexpectedly
  (the process may still be running)
- Jobs running in another Anvil process are read-only and reported as
  "Status: running in another Anvil process"
- The first read of a saved job in a new Anvil process starts from the
  beginning; wait returns at once for saved jobs
</persistence>

<tips>
- The read position is per job, so two agents reading the same job share
  it; use full=true if you lost track of earlier output
- Recommended workflow: start work with run_in_background=true, set a watch
  for its readiness line if it has one, and keep working; the completion or
  watch notification tells you when to look again
- Use wait=true only when you are blocked on the result
- For servers, use pattern (e.g. "listening on|ready") instead of sleep
  loops: wait=false to keep working, wait=true if you have nothing else to do
- If the output buffer exceeded its cap, the response notes that earlier
  output was lost
</tips>
