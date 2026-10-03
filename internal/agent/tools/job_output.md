Read output from a background job by ID; returns only new output since the previous call. Set wait=true to block until it completes, a pattern matches, or a timeout.

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
- pattern: RE2 regex, wait=true only for now; matched against stdout and
  stderr line by line, including lines that arrived before this call but
  were not yet read; on a match, returns all new output; cannot be combined
  with full=true
</parameters>

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

<tips>
- The read position is per job, so two agents reading the same job share
  it; use full=true if you lost track of earlier output
- If you have other work, keep working and check back later; use wait=true
  only when you are blocked on the result
- For servers, wait with pattern (e.g. "listening on|ready") instead of
  sleep loops
- If the output buffer exceeded its cap, the response notes that earlier
  output was lost
</tips>
