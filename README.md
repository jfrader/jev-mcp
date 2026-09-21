# Jev MCP

Jev MCP gives coding agents typed, calibrated decisions from
[TypeSafe AI's Jev](https://docs.typesafe.ai) model. Jev is a *System One* model, not a
language model: you send it a state and typed questions, and it returns structured
answers — a choice from a list, a score on a rubric, or a yes/no probability — with
confidence. It does not generate text, so it cannot write code or hold a conversation.

This is a zero-dependency Go stdio MCP server. It exposes Jev as four tools so an agent
can route, classify, rank, or gate a decision without spending reasoning tokens on it.

## Why

Agents constantly make judgments with a closed answer set: which of these files holds the
bug, is this diff risky, does this log line matter, which handler owns this request. Doing
that in the transcript costs reasoning tokens and then stays in context, where it is
re-sent on every later turn. A Jev call returns a typed answer instead.

## Requirements

- Go 1.23 or newer
- A TypeSafe API key from <https://console.typesafe.ai/keys>

## Quickstart

```sh
make build
export TYPESAFE_API_KEY=your-key
./bin/jev-mcp
```

The process reads newline-delimited MCP JSON-RPC on stdin. MCP clients normally start it
for you.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `TYPESAFE_API_KEY` | required | Bearer key for the TypeSafe API |
| `TYPESAFE_BASE_URL` | `https://api.typesafe.ai` | API base URL |
| `TYPESAFE_MODEL` | `jev-latest` | Model or alias; pin a version once thresholds are tuned |

Keep the key in the environment or a secret manager, not in a committed config file.

## MCP client configuration

Every client takes the same command and environment.

```json
{
  "mcp": {
    "jev": {
      "type": "local",
      "command": ["/absolute/path/to/jev-mcp"],
      "environment": { "TYPESAFE_API_KEY": "your-key" },
      "enabled": true
    }
  }
}
```

- **OpenCode** — an `mcp` entry in `opencode.jsonc`, as above.
- **Claude Desktop** — an `mcpServers` entry in `claude_desktop_config.json`; restart the
  app afterwards.
- **Cursor** — the same `mcpServers` shape in `.cursor/mcp.json` (per project) or the
  global Cursor MCP configuration.

## Tools

| Tool | Asks | Returns |
| --- | --- | --- |
| `ask` | several typed questions at once — prefer this | one answer per question, with probabilities and confidence |
| `choose` | one of a set | `choice`, per-option probabilities, confidence |
| `score` | where on an ordered rubric | `score`, legend, probabilities, confidence |
| `noul` | is this true? | `noul` 0–1 and a `yes` flag; no confidence |

`choose` and `score` answers carry `confidence` plus a `reliable` flag against a threshold
(0.5 by default, adjustable per call). Use it as a second axis: the answer says what,
confidence says whether to act or escalate. `ask` also lists any unreliable ids.

Write one narrow, literal condition per question and put boundary cases in the criteria.
Batching independent questions into one `ask` is far cheaper than a call per decision.

## Notes

- **What leaves your machine.** The `state` you pass and the questions go to the TypeSafe
  API. TypeSafe states that it does not train on customer requests or responses; check
  their current terms before sending anything sensitive.
- **Cost.** Input tokens only. Output is free, and many questions can share one request.
- **Limits.** Text only. Jev is not a calculator: keep counting, arithmetic, and date
  comparison in code. It reads questions literally, and accuracy drops when `state` holds
  unrelated detail. This server rejects state over 120 KB rather than send it.
- **Versioning.** The default alias moves as TypeSafe ships releases. Responses echo the
  versioned model id, so pin `TYPESAFE_MODEL` once your thresholds are tuned.
- Rate limits and overload responses are retried with backoff, honouring `Retry-After`.

## Verify

```sh
make test     # unit tests, offline; no API key or network needed
make install  # build and install to ~/.local/bin
```

## License

MIT — see [LICENSE](LICENSE).
