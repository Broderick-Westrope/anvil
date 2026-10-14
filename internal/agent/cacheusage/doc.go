// Package cacheusage records per-call LLM usage observations for prompt
// cache analysis. It normalises provider token counts, fingerprints
// requests with truncated hashes (never content), and writes rows to the
// step_usage table asynchronously so recording never blocks a call.
package cacheusage
