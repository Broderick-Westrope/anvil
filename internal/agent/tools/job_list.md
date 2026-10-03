List background jobs started by bash (run_in_background or auto-backgrounded).

<usage>
- Call with no parameters to list jobs owned by the current session
- Set all=true to list jobs from every session in this Anvil process,
  including other sessions and other agents' jobs
- Without all, the list also includes this session's jobs from earlier
  Anvil runs and from other Anvil processes; job IDs are unique across
  restarts
</usage>

<when_to_use>
- Rediscover job IDs after context loss or compaction
- Before starting a long-lived server, tunnel, or watcher, check whether
  one is already running that you can reuse
- Check which jobs are still running before finishing a task
</when_to_use>

<output>
- Running jobs are listed first, oldest first, and are never capped
- Up to 20 finished jobs follow, newest first, then a count of older
  finished jobs that were omitted
- Each line shows the job ID, status (exit code, or "killed" if the job
  was stopped rather than exiting on its own), runtime, time since last
  output (running jobs), origin (explicit or auto), description or command,
  and working directory
- Jobs running in another Anvil process are marked "(other Anvil
  process)" and can only be read, not killed, from here
- Jobs die when Anvil exits; they are then listed as "killed (when Anvil
  exited)", or as "interrupted" if Anvil exited unexpectedly
- Saved output is kept for 14 days
- Returns "No background jobs." when there are none
</output>

<tips>
- Use job_output with a listed ID to read its output
- Use job_kill with a listed ID to stop a job you no longer need
</tips>
