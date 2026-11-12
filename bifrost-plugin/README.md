# ProofPulse Bifrost plugin — local MVP

This plugin checks eligible Bifrost chat completions through the ProofPulse Go service. Non-streaming responses are checked after completion; supported streaming responses are buffered until the complete answer has been checked. The service uses Bifrost for model calls and Tavily for current web evidence. It fails open: if verification is unavailable, the original model answer is returned.

## Compatibility found on this Mac

The cached Bifrost gateway is transport `v2.5.0`, built with Go `1.27.3` and Bifrost core `v1.15.1`. The plugin must use that same Go toolchain and Bifrost core version. The module file is pinned to core `v1.15.1` for this setup.

Go plugins are platform-specific. Build this `.so` on the Mac that runs Bifrost; this machine is `darwin/arm64` and has Go `1.27.3` installed.

## Build the ProofPulse plugin

From the ProofPulse checkout:

```bash
cd ~/proofpulse/bifrost-plugin
go mod tidy
mkdir -p build
CGO_ENABLED=1 go build -buildmode=plugin -o build/proofpulse.so .
```

The output is `bifrost-plugin/build/proofpulse.so`. Do not commit the built plugin or any API keys.

## Build a plugin-capable Bifrost gateway

Bifrost's ordinary static builds cannot load Go `.so` plugins. Build the same gateway release dynamically, with the same Go version:

```bash
git clone --branch transports/v2.5.0 --depth 1 https://github.com/maximhq/bifrost.git ~/bifrost-src
cd ~/bifrost-src
make build DYNAMIC=1 VERSION=2.5.0
```

This produces `~/bifrost-src/tmp/bifrost-http`. The user's existing Bifrost databases are in `~/.config/bifrost`; start the dynamically built gateway with that same app directory so the configured OpenRouter provider and virtual key remain available:

```bash
~/bifrost-src/tmp/bifrost-http -app-dir "$HOME/.config/bifrost"
```

## Load the plugin

Bifrost reads `config.json` from its app directory. Add this `plugins` entry to `~/.config/bifrost/config.json`, using the absolute path to the `.so` file:

```json
{
  "$schema": "https://www.getbifrost.ai/schema",
  "plugins": [
    {
      "enabled": true,
      "name": "proofpulse-freshness",
      "path": "/Users/riggerlord/proofpulse/bifrost-plugin/build/proofpulse.so",
      "config": {
        "service_url": "http://127.0.0.1:8000",
        "timeout_seconds": 180
      }
    }
  ]
}
```

If `config.json` already exists, merge the `plugins` entry into it instead of replacing it. Keep the database-backed configuration enabled; do not set `config_store.enabled` to `false`, because the provider and virtual-key settings live in Bifrost's database. Never put provider or Tavily secrets in this JSON file.

When Bifrost starts, its logs should confirm that `proofpulse-freshness` loaded. Start the ProofPulse service as usual on port `8000`. Non-streaming text chat completions are checked as before. Streaming text chat completions with one choice and no tools are held until the full answer is checked; the verified answer and citations are then sent as one final content delta. This delays the first visible text until verification finishes. Tool, multimodal, multi-choice, and other unsupported streams pass through unchanged. Streaming responses carry `X-ProofPulse-Status: buffered` because response headers are committed before the final verification verdict is available; non-streaming checked responses carry `X-ProofPulse-Status: checked`.
