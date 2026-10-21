# Troubleshooting

## Logging

Sometimes you need to look at logs. Luckily, Anvil logs all sorts of
stuff. Logs are stored in `./.anvil/logs/anvil.log` relative to the project.

The CLI also contains some helper commands to make perusing recent logs easier:

```bash
# Print the last 1000 lines
anvil logs

# Print the last 500 lines
anvil logs --tail 500

# Follow logs in real time
anvil logs --follow
```

Want more logging? Run `anvil` with the `--debug` flag, or enable it in the
config:

```json
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json",
  "options": {
    "debug": true,
    "debug_lsp": true
  }
}
```

### Diagnosing memory growth after a force-quit

Anvil automatically saves memory diagnostics under
`.anvil/logs/memory/run-<timestamp>-<pid>-<unique>/` (or the configured project
state directory). No debug flag or profiling server is required. After a
force-quit, preserve that run directory and the project’s `anvil.log` before
restarting repeatedly or cleaning project state.

- `samples.jsonl` records RSS, Go heap and managed memory, allocation rate,
  goroutine count, GC statistics, event drops, PID, and build version every five
  seconds. Each sample is synced to disk; one previous 2 MiB sample file is kept.
- `baseline-*.pprof` captures startup heap and goroutine profiles. `periodic-*.pprof`
  refreshes after five minutes without another capture, including during idle waits.
- `incident-first-*.pprof` preserves the first sample at or above 512 MiB RSS,
  live heap, or estimated Go-managed resident memory. Further doublings capture
  into two rotating incident slots, at least 30 seconds apart. Matching JSON
  files record the triggering measurements.

Heap profiles contain sampled allocation stacks and both live and cumulative
allocation totals, not a dump of objects or conversation text. Goroutine
profiles show stack locations, including blocked job waits. They are written
without forcing a GC. Files are private to the current user and profiles are
capped at 8 MiB each; failed writes leave the previous complete file intact.
Runs inactive for seven days are removed when diagnostics start. Source paths
and build details can still be sensitive, so review files before sharing them.

Analyze saved profiles without a running Anvil process:

```bash
go tool pprof -top -inuse_space /path/to/run/incident-first-heap.pprof
go tool pprof -top -alloc_space /path/to/run/incident-first-heap.pprof
go tool pprof -top /path/to/run/incident-first-goroutine.pprof
```

Cumulative allocations are not current memory usage. RSS covers Anvil itself,
not subprocesses, and is sampled using `ps` on macOS/Linux. If unavailable
(including on Windows), samples contain `rss_error` and Go metrics still work.
Capture is best-effort: a sudden OOM or blocked runtime can prevent a final
sample, but completed files survive force-quitting the terminal. The recorder
needs a writable state directory; failures are reported in `anvil.log`.


## Why is clipboard copy and paste not working?

Installing an extra tool might be needed on Unix-like environments.

| Environment         | Tool                     |
| ------------------- | ------------------------ |
| Windows             | Native support           |
| macOS               | Native support           |
| Linux/BSD + Wayland | `wl-copy` and `wl-paste` |
| Linux/BSD + X11     | `xclip` or `xsel`        |
