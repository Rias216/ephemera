# Subprocess plugin protocol

Ephemera plugins are normal executables on Windows, Linux, and macOS. One process may expose multiple tools. Communication is one JSON-RPC 2.0 object per line over stdin/stdout; stdout must contain protocol messages only and diagnostics must use stderr.

## Manifest

A manifest is a JSON file with schema version `1.0`:

```json
{
  "schema_version": "1.0",
  "name": "workspace-helper",
  "version": "1.0.0",
  "command": "./workspace-helper",
  "args": [],
  "env": {"PLUGIN_MODE": "safe"},
  "cwd": ".",
  "tools": [
    {
      "name": "workspace_summary",
      "description": "Summarize the current workspace.",
      "risk": "read",
      "parameters": {
        "type": "object",
        "properties": {
          "path": {"type": "string", "description": "Workspace-relative path."}
        },
        "required": ["path"],
        "additionalProperties": false
      }
    }
  ]
}
```

`risk` must be `read`, `write`, or `shell`. Relative manifest paths, `cwd`, and path-like commands are resolved from the manifest location; bare commands use `PATH`. Environment variables in command, arguments, and environment values are expanded by the host.

## Discovery

Ephemera loads manifests from:

- each explicit `plugins.manifests` entry and repeatable `--tool <manifest.json>` argument;
- each configured `plugins.directories` directory;
- `<workspace>/.ephemera/plugins/*.json`;
- the user configuration directory under `ephemera/plugins/*.json`.

Tool names must not collide with built-ins, MCP tools, or another plugin in the same runtime registry.

## Initialize

The first host request is:

```json
{"jsonrpc":"2.0","id":"workspace-helper-1","method":"initialize","params":{"protocol_version":"1.0","workspace":"/workspace","host":{"name":"ephemera","os":"windows","arch":"amd64"}}}
```

The plugin must answer with the same ID and JSON-RPC version:

```json
{"jsonrpc":"2.0","id":"workspace-helper-1","result":{"protocol_version":"1.0","name":"workspace-helper","version":"1.0.0"}}
```

A protocol mismatch, malformed response, wrong ID, timeout, or early process exit rejects the plugin and closes the process.

## Tool call

The host sends:

```json
{"jsonrpc":"2.0","id":"workspace-helper-2","method":"call","params":{"tool":"workspace_summary","arguments":{"path":"."}}}
```

A successful result is:

```json
{"jsonrpc":"2.0","id":"workspace-helper-2","result":{"ok":true,"output":"summary text","metadata":{"files":12}}}
```

A tool-level failure uses `ok:false` and `error`. A protocol-level failure uses the JSON-RPC `error` object. Calls are bounded by the configured agent tool timeout; timeout or cancellation terminates the child process. Registry shutdown also terminates the process.
