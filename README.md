# OpenCode Go CLIProxyAPI Plugin

A native dynamic Go plugin for [CLIProxyAPI](https://help.router-for.me/plugin/development) that exposes OpenCode Go as a single provider (`opencode-go`).

The plugin unifies model discovery, protocol translation, and execution across OpenCode Go's upstream endpoints while leveraging CLIProxyAPI's built-in authentication, scheduling, keys rotation, and cooldown management.

## The Problem

OpenCode Go exposes models across multiple API protocols (OpenAI Chat Completions `/openai/v1/chat/completions`, Anthropic Messages `/anthropic/v1/messages`, and OpenAI Responses `/openai/v1/responses`, all under `https://opencode.ai/inference/go`).

Without this plugin, using OpenCode Go in CLIProxyAPI requires configuring separate provider blocks for each protocol family. This leads to:
- **Duplicated configuration & keys**: The same API keys must be configured across multiple provider blocks.
- **Fragmented scheduling & rotation**: Keys rotation, rate limits, and cooldowns cannot be shared across protocols—exhausting quota on one protocol does not coordinate with another.
- **Client protocol burden**: Clients must know beforehand which upstream protocol and endpoint each model requires.
- **Fragmented catalog**: Models are split across disjoint provider namespaces instead of a unified model list.

## The Solution

This plugin exposes OpenCode Go as a single provider (`opencode-go`) backed by a shared keys pool:
- **Unified auth pool**: Configure keys once; CLIProxyAPI schedules, rotates, and cools down keys across all protocols.
- **Transparent protocol translation & routing**: Clients request models (e.g. `opencode-go/glm-5.2`, `opencode-go/gpt-5.6-luna`) without needing to know the upstream protocol format.
- **Single model catalog**: All models are discovered and published under the `opencode-go` provider namespace in `/v1/models`.

## Features

- **Single Provider Namespace**: Exposes models under the `opencode-go` provider prefix (e.g. `opencode-go/glm-5.2`, `opencode-go/qwen3.7-max`, `opencode-go/gpt-5.6-luna`).
- **Multi-Protocol Translation**: Translates requests and streaming responses between client formats and upstream endpoints:
  - OpenAI Chat Completions (`/v1/chat/completions`)
  - Anthropic Messages (`/v1/messages`)
  - OpenAI Responses (`/v1/responses`)
- **Thinking & Reasoning Support**: Maps reasoning effort across supported client and upstream formats.
- **Docs-Driven Catalog**: Reads models, upstream URLs, and protocols from the Endpoints table of the [OpenCode Go docs](https://opencode.ai/v2/docs/console/go#endpoints), refreshes it periodically, and keeps the last good snapshot when a refresh fails.
- **Multi-Key Auth Scheduling**: Pools multiple API keys with CLIProxyAPI's native scheduler for rotation, retries, and error cooldowns across all protocols.
- **Native quotas**: Rolling, weekly, and monthly quotas use CLIProxyAPI's generic quota endpoints and the selected credential. The separate plugin page is removed.

## Requirements

- **CLIProxyAPI**: `v8.0.0+`
- **Go Toolchain**: Go 1.26.7+ (with CGO enabled for C-shared build mode)

## Build

Build the shared library with the `debug` tag for local development.
CI handles release builds.

### Windows (AMD64)
```powershell
go build -tags debug -buildmode=c-shared -o plugins/windows/amd64/opencode-go-cliproxyapi.dll .
```

### Linux (AMD64)
```bash
go build -tags debug -buildmode=c-shared -o plugins/linux/amd64/opencode-go-cliproxyapi.so .
```

### macOS (ARM64)
```bash
go build -tags debug -buildmode=c-shared -o plugins/darwin/arm64/opencode-go-cliproxyapi.dylib .
```

Place the compiled binary into your CLIProxyAPI plugin directory (e.g. `<cliproxyapi_root>/plugins/<os>/<arch>/`).

## Configuration

Configure the plugin in your CLIProxyAPI `config.yaml` under `plugins.configs.opencode-go-cliproxyapi`:

```yaml
plugins:
  configs:
    opencode-go-cliproxyapi:
      # Docs page whose Endpoints table defines models and routes
      # (default: "https://opencode.ai/v2/docs/console/go")
      # models-doc-url: "https://opencode.ai/v2/docs/console/go"

      # Quota usage endpoint (default: "https://opencode.ai/inference/go/v1/usage")
      # usage-url: "https://opencode.ai/inference/go/v1/usage"

      # Client-facing model ID prefix configuration
      model-prefix:
        enabled: true           # true -> "opencode-go/<model>", false -> bare "<model>" (default: true)
        value: "opencode-go"    # prefix name (default: "opencode-go")

      # OpenCode Go API keys (at least one required). Supports ${ENV_VAR} expansion.
      api-keys:
        - value: "sk-opencode-key-1"
          name: "Personal"      # optional account label
        - value: "sk-opencode-key-2"
        - value: "${OPENCODE_GO_API_KEY}"

      # Catalog discovery settings
      catalog:
        refresh-interval: "15m"          # docs page refresh cadence, min "1m" (default: "15m")
        stale-while-unavailable: true    # retain last good catalog snapshot on refresh failure (default: true)

      # Protocol enable/disable switches (all default to true)
      protocols:
        chat-completions: true   # enables models routed to /openai/v1/chat/completions
        messages: true           # enables models routed to /anthropic/v1/messages
        responses: true          # enables models routed to /openai/v1/responses

      # Execution settings
      request-timeout: "5m"              # upstream request timeout (default: "5m")
      max-response-bytes: 67108864       # max non-streaming response body size in bytes (default: 64 MiB)
      allow-http: false                  # allow http:// scheme for local mock/testing (default: false)
```

### Configuration Options

| Option | Type | Default | Description |
|---|---|---|---|
| `api-keys` | `[]object` | *(Required)* | List of API keys (`- value: "..."`, optional `name: "Personal"`). Supports `${ENV_VAR}` expansion. Duplicates and empty values are rejected. |
| `models-doc-url` | `string` | `https://opencode.ai/v2/docs/console/go` | Docs page whose Endpoints table lists each model's ID, upstream URL, and AI SDK package. Only rows whose URL is on this page's host are routed. Must be valid HTTPS (or HTTP if `allow-http: true`) without query parameters, fragments, or userinfo. |
| `usage-url` | `string` | `https://opencode.ai/inference/go/v1/usage` | Quota usage endpoint. Same URL rules as `models-doc-url`. |
| `model-prefix.enabled` | `bool` | `true` | When `true`, client-facing model names use `<prefix>/<model>`. When `false`, uses bare model IDs. |
| `model-prefix.value` | `string` | `opencode-go` | Provider prefix string when prefixing is enabled. |
| `catalog.refresh-interval` | `duration` | `15m` | Interval between docs page refreshes (e.g. `15m`, `1h`). Minimum is `1m`. |
| `catalog.stale-while-unavailable` | `bool` | `true` | When `true`, serves the last valid catalog snapshot if an update fails. |
| `protocols.chat-completions` | `bool` | `true` | Protocol switch for Chat Completions endpoints. |
| `protocols.messages` | `bool` | `true` | Protocol switch for Messages endpoints. |
| `protocols.responses` | `bool` | `true` | Protocol switch for Responses endpoints. |
| `request-timeout` | `duration` | `5m` | Upstream HTTP request timeout. Must be positive. |
| `max-response-bytes` | `int64` | `67108864` (64 MiB) | Maximum non-streaming response body size in bytes. |
| `allow-http` | `bool` | `false` | When `true`, permits `http://` scheme in `models-doc-url` / `usage-url` for local testing. |

## Testing

```powershell
# Run all tests
go test -tags debug ./...

# Run tests with coverage
go test -tags debug ./... -cover

# Run linter / vetting
go vet ./...
```

## Account names and quotas

Set an optional `name` on each `api-keys` entry, alongside `value`.
For example, use `name: Personal` or `name: Work`. Without a name, one key
uses **OpenCode Go**; multiple keys use **OpenCode Go 1**, **OpenCode Go 2**, and subsequent numbers.
Default numbers follow configuration order. Explicit names remain stable when keys are reordered.
The usage response contains no email or account identity. The plugin does not infer an email from a key.

New credential files have readable names. Existing auth IDs remain unchanged, and
legacy generated labels are replaced when parsed. Existing custom labels are preserved
unless configuration specifies a name. Existing filenames are retained automatically;
stop CLIProxyAPI before renaming an old file to a readable `.json` filename, and keep
its JSON `id` and all other fields unchanged. Back up the file first.

Discover support with `GET /v0/management/quota/providers`, then call
`POST /v0/management/quota/fetch` with `{"auth_index":"<selected index>"}`.
These endpoints require the CLIProxyAPI management key. The normalized response contains
`subscription.plan`, `groups[].buckets[].window`, `remainingFraction`, and `resetTime`.
Missing windows are omitted. Invalid readings return an error, not an invented zero.
Quota reset is unsupported. The old plugin quota route and resource page are removed.
