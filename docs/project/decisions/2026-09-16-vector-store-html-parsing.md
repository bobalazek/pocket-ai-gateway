# ADR-048: Bounded HTML parsing for Vector Stores

Status: Accepted

OpenAI File Search accepts HTML documents. Pocket AI Gateway parses `.html` files locally with the maintained HTML parser already present in the dependency graph, caps source and derived text at 16 MiB, preserves block boundaries, and excludes head, script, style, template, and noscript content. Parsed plaintext is derived on request and is not stored separately.

Malformed HTML follows browser-style recovery. CSS visibility, linked resources, and JavaScript-rendered content are not evaluated.
