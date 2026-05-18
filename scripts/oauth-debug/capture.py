"""mitmproxy addon that captures Anthropic API request details.

Usage:
    mitmdump -s capture.py --set outfile=capture.json

Filters for requests to api.anthropic.com and writes a structured
JSON snapshot of each request's headers, query params, URL, and
relevant body fields (system messages, model, beta flags).

Multiple requests are collected into a JSON array. The output file
is overwritten on each run.
"""

import json
import os
from datetime import datetime, timezone

from mitmproxy import http

# Accumulate all captured requests in memory, flush on shutdown.
_captures: list[dict] = []
_outfile: str = "capture.json"


def load(loader):
    loader.add_option(
        name="outfile",
        typespec=str,
        default="capture.json",
        help="Output file for captured request snapshots.",
    )


def configure(updated):
    global _outfile
    from mitmproxy import ctx

    _outfile = ctx.options.outfile


def request(flow: http.HTTPFlow) -> None:
    req = flow.request

    # Only capture Anthropic API requests.
    if "api.anthropic.com" not in req.pretty_host:
        return

    # Parse request body for relevant fields.
    body_fields = {}
    if req.content:
        try:
            body = json.loads(req.content)
            # Extract model, system messages, and stream flag — skip
            # user message content to avoid leaking prompt data.
            if "model" in body:
                body_fields["model"] = body["model"]
            if "stream" in body:
                body_fields["stream"] = body["stream"]
            if "max_tokens" in body:
                body_fields["max_tokens"] = body["max_tokens"]

            # Extract system messages (billing header, identity prefix).
            if "system" in body:
                system = body["system"]
                if isinstance(system, list):
                    # Array of {type, text} objects.
                    body_fields["system_messages"] = [
                        entry.get("text", str(entry))
                        for entry in system
                        if isinstance(entry, dict)
                    ]
                elif isinstance(system, str):
                    body_fields["system_messages"] = [system]
        except (json.JSONDecodeError, UnicodeDecodeError):
            body_fields["_parse_error"] = True

    # Build header dict, preserving case.
    headers = dict(req.headers)

    # Redact the actual bearer token but keep the prefix.
    if "Authorization" in headers:
        val = headers["Authorization"]
        if val.startswith("Bearer ") and len(val) > 20:
            headers["Authorization"] = val[:20] + "...<redacted>"

    # Parse query params.
    query_params = dict(req.query)

    snapshot = {
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "method": req.method,
        "url": req.url.split("?")[0],  # Base URL without query.
        "path": req.path.split("?")[0],
        "query_params": query_params,
        "headers": headers,
        "body": body_fields,
    }

    _captures.append(snapshot)

    from mitmproxy import ctx

    ctx.log.info(
        f"[oauth-debug] Captured {req.method} {req.url.split('?')[0]} "
        f"({len(headers)} headers, model={body_fields.get('model', '?')})"
    )


def done():
    """Write all captures to disk on shutdown."""
    if not _captures:
        from mitmproxy import ctx

        ctx.log.warn("[oauth-debug] No Anthropic requests captured.")
        return

    with open(_outfile, "w") as f:
        json.dump(_captures, f, indent=2)

    from mitmproxy import ctx

    ctx.log.info(
        f"[oauth-debug] Wrote {len(_captures)} request(s) to {_outfile}"
    )
