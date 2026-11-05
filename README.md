# ProofPulse

ProofPulse is a Go freshness layer for Bifrost, powered by Tavily. The Go service checks up to three time-sensitive claims against live web evidence and returns claim verdicts, source links, and a corrected answer when the evidence supports one. A separate Bifrost HTTP plugin applies that verification to eligible gateway chat-completion responses.

## Run locally

Requirements: Go 1.22 or later for the ProofPulse service, a running Bifrost gateway with a model provider configured, and a Tavily API key.

1. Copy `.env.example` to `.env` and set `TAVILY_API_KEY`. Keep `.env` private.
2. Confirm Bifrost is running at `http://localhost:8080/v1`. The defaults use the `openrouter/free` model; set `BIFROST_MODEL` to a model configured in your Bifrost instance.
3. Start ProofPulse:

   ```bash
   go run ./cmd/proofpulse
   ```

4. Open <http://localhost:8000>.

Build a standalone server binary with:

```bash
go build -o proofpulse ./cmd/proofpulse
```

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `ADDR` | `:8000` | HTTP listen address |
| `BIFROST_BASE_URL` | `http://localhost:8080/v1` | Bifrost OpenAI-compatible API base URL |
| `BIFROST_API_KEY` | `local-development` | Key accepted by the local Bifrost gateway, if configured |
| `BIFROST_MODEL` | `openrouter/free` | Model name configured in Bifrost |
| `TAVILY_API_KEY` | — | Tavily search key; required for live verification |

## Request flow

```text
Bifrost chat completion → ProofPulse plugin → ProofPulse Go API
                        → Bifrost claim analysis → Tavily evidence → verdicts/citations
                        → verified chat completion
```

The API is `POST /api/verify` with JSON `{ "question": "...", "answer": "optional existing answer" }`. `GET /health` is a lightweight health check. The web page is embedded into the Go binary. The service marks up to three important time-sensitive claims for verification to keep latency bounded.

## Build and configure the Bifrost plugin

The plugin in `bifrost-plugin/` intercepts buffered `/chat/completions` responses, sends the user's text and model answer to the ProofPulse service, and adds the verified answer with evidence links. It skips streaming calls and internal ProofPulse model calls. If the service is unavailable, Bifrost returns its original answer unchanged.

Build the shared object with the same Go version used to build your Bifrost gateway (the module currently pins Bifrost core `v1.13.1` and Go `1.27.3`):

```bash
cd bifrost-plugin
go mod tidy
mkdir -p build
go build -buildmode=plugin -o build/proofpulse.so .
```

Enable the `.so` as a custom plugin in Bifrost and set its config to the ProofPulse service URL. For Bifrost running in Docker on macOS, the service URL is typically `http://host.docker.internal:8000`; for a native Bifrost process, use `http://127.0.0.1:8000`.

```yaml
bifrost:
  plugins:
    custom:
      - name: proofpulse-freshness
        enabled: true
        path: /plugins/proofpulse.so
        config:
          service_url: http://host.docker.internal:8000
          timeout_seconds: 180
```

The plugin must match Bifrost's Go version, OS, and architecture, and Bifrost must use a dynamic build that enables `.so` loading. Follow the [official dynamic-build guide](https://github.com/maximhq/bifrost/blob/dev/docs/plugins/building-dynamic-binary.mdx) for the gateway setup. Current Go plugins support Linux and macOS. Streaming chat responses are passed through without freshness checks in this first version.

## Verification behavior

ProofPulse marks a claim stale only when the evidence clearly contradicts it. An unavailable search or an unclear model verdict is reported as uncertain, and the original answer is preserved unless a specific correction is supported by the evidence.
