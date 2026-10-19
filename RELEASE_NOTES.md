## What's Changed

### Features

- Read models and routes from the Endpoints table of the OpenCode Go docs page (`https://opencode.ai/v2/docs/console/go`). Each row's AI SDK package selects the protocol and its URL is used as-is, so new models work without a plugin update. Rows that point off the docs host, or use a package without an adapter, are excluded and logged.

### Bug Fixes

- Move upstream calls to the OpenCode Go inference API (`https://opencode.ai/inference/go`): Chat Completions and Responses use `/openai/v1/...`, Messages uses `/anthropic/v1/messages`, and quota uses `/v1/usage`.
- Route `claude-haiku-5-5`, `space-bunny`, `step-5-preview`, and `beam-free`, which the old built-in family table missed.

- Preserve Claude image tool results when routing to Chat Completions: retain images in an associated user message after tool replies, preserve text, tool references and error markers, and include images in session derivation. The selected upstream model must support image input.

Special thanks to @c0mm4nd in #16.

- Fix `tool_result` translation when receiving `tool_reference` content blocks from Claude clients (such as ToolSearch, web search, or web fetch in Claude Code), preventing 500 errors and conversation lockouts on Chat Completions and Responses models like DeepSeek.

Special thanks to @SpiGAndromeda in #14.

## Upgrade Notes

The `base-url`, `catalog-url`, and `route-overrides` options are removed and ignored if present.
Use `models-doc-url` and `usage-url` only to point at a mirror; the defaults need no configuration.
Models no longer publish context length, modality, or thinking metadata, because the docs table does not provide it (the live `/v1/models` never did either).

- Replace the old plugin binary with the new release binary.
- Restart CLIProxyAPI after replacing the plugin.

**Full Changelog**: https://github.com/massiveits/opencode-go-cliproxyapi/compare/v0.2.1...v0.2.2
