# codex-proxy

A small local OpenAI-compatible proxy backed by your existing Codex CLI ChatGPT login.

```text
OpenAI-compatible client -> http://127.0.0.1:6769/v1 -> ChatGPT Codex backend
```

This is a compatibility adapter, not a transparent OpenAI proxy. The Codex backend currently requires streaming upstream requests and rejects some common OpenAI parameters, so `codex-proxy` normalizes requests before forwarding them.

## Requirements

- Codex CLI authenticated with ChatGPT:

```bash
codex login
```

- Go 1.22+ only if building from source

## Install

macOS/Linux users can install the latest main build directly:

```bash
curl -fsSL https://raw.githubusercontent.com/Max-Leopold/codex-proxy/main/scripts/install.sh | bash
```

By default this installs to `/usr/local/bin`. To install somewhere else:

```bash
curl -fsSL https://raw.githubusercontent.com/Max-Leopold/codex-proxy/main/scripts/install.sh | INSTALL_DIR="$HOME/.local/bin" bash
```

Downloadable binaries for macOS, Linux, and Windows are published on the [GitHub Releases page](https://github.com/Max-Leopold/codex-proxy/releases).

## Run

```bash
codex-proxy --port 6769
```

By default the server binds to `127.0.0.1` with no proxy API key.

To listen on a non-loopback interface, you must set a proxy API key. Prefer the environment variable on shared systems because `--api-key` can appear in shell history and process lists:

```bash
CODEX_PROXY_API_KEY='replace-with-a-long-random-key' codex-proxy --host 0.0.0.0 --port 6769
```

You can also pass `--api-key` directly. OpenAI-compatible clients should set `OPENAI_API_KEY` to the same value; they will send it as `Authorization: Bearer <OPENAI_API_KEY>`.

## Build from source

```bash
go run . --port 6769
```

or:

```bash
go build -o codex-proxy .
./codex-proxy --port 6769
```

## Client configuration

Most OpenAI-compatible clients can use:

```bash
export OPENAI_BASE_URL=http://127.0.0.1:6769/v1
export OPENAI_API_KEY=dummy
```

The proxy ignores the incoming API key unless a proxy API key is configured; then `OPENAI_API_KEY` must match the proxy API key.

Logs include request metadata, status, bytes, and duration. Request and response bodies are not logged.

## Examples

If a proxy API key is configured, add `-H 'Authorization: Bearer <api-key>'` to these `curl` examples.

List models:

```bash
curl http://127.0.0.1:6769/v1/models
```

Responses API:

```bash
curl http://127.0.0.1:6769/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.4-mini","input":"Reply with exactly: pong"}'
```

Streaming Responses API:

```bash
curl http://127.0.0.1:6769/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.4-mini","stream":true,"input":"Reply with exactly: pong"}'
```

Chat Completions API:

```bash
curl http://127.0.0.1:6769/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.4-mini","messages":[{"role":"user","content":"Reply with exactly: pong"}]}'
```

Streaming chat completions:

```bash
curl http://127.0.0.1:6769/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.4-mini","stream":true,"messages":[{"role":"user","content":"Reply with exactly: pong"}]}'
```

## Remote access

By default `codex-proxy` only listens on `127.0.0.1`. The safest way to use it remotely is still an SSH tunnel:

```bash
ssh -L 6769:127.0.0.1:6769 user@your-vps
```

If you intentionally expose it from a VPS, bind to a public interface and require a proxy API key:

```bash
CODEX_PROXY_API_KEY='replace-with-a-long-random-key' codex-proxy --host 0.0.0.0 --port 6769
```

## Supported routes

- `GET /healthz`
- `GET /v1/models`
- `POST /v1/responses`
- `POST /v1/chat/completions`

## Current limitations

Unsupported:

- embeddings
- images generation
- audio
- files
- batches
- fine-tuning
- full optional-field parity across every OpenAI response variant

Function tools, `tool_choice`, and chat `response_format` are translated where the Codex backend supports them.

Some OpenAI parameters are accepted from clients but intentionally stripped before the Codex backend call, including `temperature`, `top_p`, `stop`, `max_tokens`, `max_output_tokens`, `max_completion_tokens`, and `user`. Upstream `store` is always forced to `false`.
