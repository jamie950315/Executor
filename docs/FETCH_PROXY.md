# Fetch request proxy

The `fetch-proxy` branch adds a tenth MCP tool, `fetch`, alongside the original nine tools. It executes one existing tool call packaged as a string. The original tools and their schemas remain available.

## Recommended usage

Call the MCP tool named `fetch` with one required argument, `request`. The value is a JSON object encoded as a string:

```json
{
  "request": "{\"name\":\"filesystem_write\",\"arguments\":{\"action\":\"write_file\",\"privilege\":\"owner\",\"path\":\"/tmp/executor-demo.txt\",\"content\":\"Hello 中文\\n\"}}"
}
```

For a JavaScript MCP client, generate the string with `JSON.stringify` to preserve quotes, line breaks, Unicode, and special characters:

```javascript
const operation = {
  name: "filesystem_write",
  arguments: {
    action: "append_file",
    privilege: "owner",
    path: "/tmp/executor-demo.txt",
    content: "Another line 中文\n",
  },
};

await mcpClient.callTool({
  name: "fetch",
  arguments: { request: JSON.stringify(operation) },
});
```

Use a path appropriate for the target OS. All original arguments, including `privilege`, `encoding`, `argv`, pagination bounds, and desktop `captureId`, belong inside `arguments`. Consult the target tool's existing `tools/list` schema for its required inputs.

## URI-shaped input

The same operation can be packaged as a local URI-shaped request string:

```javascript
const request =
  "executor://call?request=" + encodeURIComponent(JSON.stringify(operation));

await mcpClient.callTool({
  name: "fetch",
  arguments: { request },
});
```

`executor://call` is an in-process request identifier. Pass it as the MCP argument shown above. Executor decodes it locally without fetching a URL or opening a browser. The URI has exactly one `request` query parameter; encode the entire JSON value once. Literal `+` characters must be encoded as `%2B`.

Both forms use the existing authenticated `POST /mcp` transport or local stdio transport. There is no additional HTTP endpoint or network configuration to deploy.

## Wire format

After the normal MCP initialization, an authenticated HTTP client sends this body to `/mcp` with its existing bearer token and `Mcp-Session-Id` header:

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "tools/call",
  "params": {
    "name": "fetch",
    "arguments": {
      "request": "{\"name\":\"filesystem_write\",\"arguments\":{\"action\":\"write_file\",\"privilege\":\"owner\",\"path\":\"/tmp/executor-demo.txt\",\"content\":\"Hello\"}}"
    }
  }
}
```

An ordinary successful write returns the same byte counts and operation metadata as `filesystem_write`; the outer `toolName` is `fetch`. Screenshots retain their image blocks. Tool failures retain standard MCP `isError` content. Each request dispatches at most one tool call; the proxy adds no automatic retry, deduplication, rollback, or transaction. Repeating an append or command invocation can repeat its effects. A lost connection leaves the operation outcome unconfirmed; inspect the target state before deciding whether to repeat it.

## Authorization and validation

- `fetch` explicitly advertises `readOnlyHint: false` and `destructiveHint: true`. Clients must authorize it as a mutating tool. This feature adds request-format compatibility; client-side permission and confirmation rules remain applicable.
- HTTP calls retain the existing OAuth scope, Origin validation, session checks, and disabled-state check. stdio retains its existing local authorization model. The resolved call reaches the existing dispatcher and its owner/admin helpers with the original context and session ID.
- The target must be present in this server's advertised tool catalog. Private Dashboard operations and recursive `fetch` calls are rejected. No additional shell, filesystem, or network execution implementation is introduced.
- The string is limited to 8,388,608 UTF-8 bytes, including any URI escaping. Use the original tools for larger requests. The envelope requires exactly `name` and `arguments`; unknown or duplicate envelope fields, missing/non-object arguments, extra query parameters, malformed encoding, and trailing JSON are rejected. The target tool keeps its existing argument validation and JSON value semantics.
- Parser errors omit the submitted content. Valid decoded operations reach the existing metadata-only audit wrapper as the actual target tool with its privilege and session. File contents, request strings, credentials, and command output remain outside that audit. Keep credentials in the normal authentication channel and never put the URI-shaped request in a public URL or log.
- `GET`, `HEAD`, and `OPTIONS` on `/mcp` keep their existing non-executing behavior. The proxy has no network-fetch capability.

## Approach selection

| Approach | Benefit | Cost or constraint |
| --- | --- | --- |
| MCP `fetch(request)` with JSON | Reuses authentication, sessions, audit, and image/error results; straightforward encoding | Requires a client permitted to invoke mutating MCP tools |
| MCP `fetch(request)` with a local URI | Supports query-shaped string packaging through the same implementation | Percent encoding increases the request size |
| Separate HTTP endpoint | Convenient for a separate REST client | Adds another authentication, session, and result-format surface to maintain |

The first form is the default recommendation. The second form provides the requested query-string packaging without introducing a separate public API. URL reads that execute commands would create repeat-execution and accidental-trigger hazards and place payloads in URL-handling systems; this branch keeps execution on the existing explicit tool-call path.

## Verification and deployment boundary

Run:

```bash
go test ./...
go test -race ./internal/mcp ./internal/daemon ./internal/dispatch ./internal/agent
go test ./internal/mcp -run '^$' -fuzz FuzzFetchEnvelope -fuzztime=8s -parallel=2
go vet ./...
go build ./...
```

The integration test starts isolated Agent, Broker, and Desktop helpers. It verifies authenticated HTTP and stdio file writes, Unicode append/read/move/delete, owner/admin routing, actual process output and exit code, bounded terminal reads, authentication/Origin/session rejection, disabled-state rejection, and metadata-only audit. Both test helpers run with the test process's OS identity; elevated OS privileges remain a separate deployment check. No real pointer or keyboard input is sent.

During implementation, the full Go suite, `go vet`, `go build`, and the isolated runtime checks passed on macOS. Both `executor` and `executor-kill` cross-built for macOS, Linux, and Windows on amd64 and arm64; macOS builds enabled native CGO. A separate race/fuzz command was started, but the platform blocked retrieval of its result; that run remains unconfirmed and is excluded from passed-verification claims.

Installed production and Beta services, configuration, credentials, and tunnels are unchanged by this source change. Cross-compilation establishes build compatibility; Windows/Linux runtime checks and actual ChatGPT invocation require their respective target environments. Deploying or restarting an existing installation remains a separate operation.
