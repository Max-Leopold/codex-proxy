# codex-proxy

A small local OpenAI-compatible proxy backed by your existing Codex CLI ChatGPT login.

```text
OpenAI-compatible client -> http://127.0.0.1:6769/v1 -> ChatGPT Codex backend
```

This is a compatibility adapter, not a transparent OpenAI proxy. The Codex backend currently requires streaming upstream requests and rejects some common OpenAI parameters, so `codex-proxy` normalizes requests before forwarding them.

## Requirements

- Go 1.22+
- Codex CLI authenticated with ChatGPT:

```bash
codex login
```

## Run

```bash
go run . --port 6769
```

or:

```bash
go build -o codex-proxy .
./codex-proxy --port 6769
```

The server always binds to `127.0.0.1`.

## Client configuration

Most OpenAI-compatible clients can use:

```bash
export OPENAI_BASE_URL=http://127.0.0.1:6769/v1
export OPENAI_API_KEY=dummy
```

The proxy ignores the incoming API key; many clients just require that one is set.

Logs include request metadata, status, bytes, and duration. Request and response bodies are not logged.

## Examples

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

`codex-proxy` only listens on `127.0.0.1`. If you run it on a VPS and need to use it from your laptop, use an SSH tunnel instead of exposing the proxy publicly:

```bash
ssh -L 6769:127.0.0.1:6769 user@your-vps
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
