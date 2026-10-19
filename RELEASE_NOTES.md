## What's Changed

### Bug Fixes

- Move upstream calls to the OpenCode Go inference API (`https://opencode.ai/inference/go`): Chat Completions and Responses use `/openai/v1/...`, Messages uses `/anthropic/v1/messages`, and the catalog and quota use `/v1/models` and `/v1/usage`.

- Preserve Claude image tool results when routing to Chat Completions: retain images in an associated user message after tool replies, preserve text, tool references and error markers, and include images in session derivation. The selected upstream model must support image input.

Special thanks to @c0mm4nd in #16.

- Fix `tool_result` translation when receiving `tool_reference` content blocks from Claude clients (such as ToolSearch, web search, or web fetch in Claude Code), preventing 500 errors and conversation lockouts on Chat Completions and Responses models like DeepSeek.

Special thanks to @SpiGAndromeda in #14.

## Upgrade Notes

If your config sets `base-url: https://opencode.ai/zen/go/v1`, change it to `https://opencode.ai/inference/go` or remove it to use the default.
The old value is no longer supported and produces wrong upstream paths.
Route override endpoints are now appended to `base-url` as-is, so write them with the protocol prefix (for example `/anthropic/v1/messages`).

- Replace the old plugin binary with the new release binary.
- Restart CLIProxyAPI after replacing the plugin.

**Full Changelog**: https://github.com/massiveits/opencode-go-cliproxyapi/compare/v0.2.1...v0.2.2
