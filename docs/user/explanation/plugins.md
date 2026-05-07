# Plugins (preview)

azldev supports loading **out-of-process plugins** that extend the CLI with
new commands. Plugins are stand-alone executables that speak the
[Model Context Protocol](https://modelcontextprotocol.io/) (MCP) over their
standard I/O — the same protocol azldev itself speaks when run as
`azldev advanced mcp`. This means a single plugin binary can be consumed by
azldev *and* by AI coding agents, with no change to the plugin.

> **Status:** preview. The current implementation supports loading
> plugins from the command line, registering their tools as Cobra
> subcommands, optionally grafting tools into the existing command
> hierarchy via per-tool metadata, and a small admin CLI for
> introspection. Future phases will add user-level config registration,
> project-config injection, and named provider/backend extensions.

## Loading a plugin

Today, plugins are registered via the `--plugin` flag, which may be
repeated:

```sh
azldev --plugin /path/to/azldev-plugin-foo --plugin /path/to/azldev-plugin-bar ...
```

For each `--plugin <path>`:

1. azldev spawns the binary as a subprocess.
2. It performs the MCP `initialize` handshake to learn the plugin's
   self-reported name and version.
3. It calls `tools/list` to discover the tools the plugin advertises.
4. Each tool is registered as a Cobra subcommand under
   `azldev plugin <plugin-name> <tool-name>`.

The plugin process stays alive for the duration of the `azldev` invocation
and is shut down before exit.

## Discovering plugin commands

After loading, `azldev --help` shows a `Plugin commands:` group with a
`plugin` entry. Drill in to see what's available:

```sh
azldev --plugin ./my-plugin --help          # 'plugin' appears in the group
azldev --plugin ./my-plugin plugin --help   # lists each loaded plugin by name
azldev --plugin ./my-plugin plugin foo --help        # lists tools in plugin 'foo'
azldev --plugin ./my-plugin plugin foo greet --help  # shows tool flags
```

## Tool inputs become Cobra flags

azldev derives Cobra flags from each tool's MCP **input schema**:

| JSON Schema type | Cobra flag        |
|------------------|-------------------|
| `string`         | `String`          |
| `integer`        | `Int64`           |
| `number`         | `Float64`         |
| `boolean`        | `Bool`            |

Properties listed in the schema's `required` array become required flags
(via `MarkFlagRequired`). Properties with a `default` keyword propagate
that default into Cobra. The tool's `description` becomes the flag help
string.

Tools whose input schemas use unsupported features (objects, arrays,
`oneOf`/`anyOf`, multi-type, etc.) are **skipped with a warning** during
registration — the rest of the plugin's tools remain usable. Future
phases will widen the supported subset and add a manifest mechanism for
declaring richer flag shapes.

### Default-value semantics

If you don't pass a flag on the command line, azldev *omits* it from the
MCP `tools/call` request. This lets the plugin's own schema-declared
default take effect, rather than the Cobra-side type-zero default
silently overriding it.

## Errors and fix suggestions

Plugin failures fall into five distinct categories, each surfaced with a
tailored fix suggestion:

| Category                 | Trigger                                                        |
|--------------------------|----------------------------------------------------------------|
| spawn failed             | binary not found, not executable, immediate exec failure       |
| handshake failed         | MCP `initialize` rejected or transport broken before reply     |
| tool discovery failed    | `tools/list` returned a transport/protocol error               |
| call transport failed    | tool invocation transport error or protocol violation          |
| tool reported error      | tool ran but returned `isError: true` in its result            |

A bad `--plugin` path is fail-fast: azldev refuses to continue rather than
silently dropping the missing plugin.

## Authoring a plugin

Any executable that speaks MCP over stdio can serve as an azldev plugin.
The reference plugin under `cmd/azldev-plugin-hello/` is a minimal
working example; it advertises a single `greet` tool. A future SDK
package will provide higher-level helpers for authoring plugins.

## Out of scope (in MVP)

The current implementation deliberately omits:

- **TOML / XDG configuration.** Plugins must be supplied via `--plugin`;
  no `[plugins.<name>]` config section yet.
- **Plugin settings.** Plugins do not yet receive a configuration blob;
  they run with whatever args/env they spawn under.
- **Project-config injection.** Plugins cannot yet read the resolved
  project config. (They could read TOML themselves, but azldev does not
  pass it.)
## Manifest resource (`azldev://manifest`)

Plugins may publish a single MCP resource at the well-known URI
`azldev://manifest` to advertise plugin-wide metadata to azldev. The
resource body is JSON; the supported fields today are:

```jsonc
{
  "protocol-version": 1,                 // required; only 1 is supported today
  "title": "My plugin",                  // optional; overrides plugin display name
  "description": "What the plugin does." // optional; long description
}
```

If the manifest is absent or doesn't parse, azldev falls back to MCP's
`serverInfo` (name + version) for plugin display. If the manifest *is*
present but advertises an unsupported `protocol-version`, the plugin is
rejected with a clear upgrade hint.

The reference plugin under `cmd/azldev-plugin-hello/` shows the minimal
working shape.

## Per-tool placement: `_meta.azldev`

azldev grafts a tool into the existing Cobra command hierarchy when the
tool's MCP `_meta` object includes an `azldev` block. The supported keys
today are:

| Key            | Type     | Purpose                                                          |
|----------------|----------|------------------------------------------------------------------|
| `command-path` | string[] | Cobra path at which to graft the tool (e.g. `["component","cb"]`)|
| `aliases`      | string[] | Additional names the leaf can be invoked by                      |
| `hidden`       | bool     | Hide from default `--help` output (still invokable)              |
| `group-title`  | string   | Used only when introducing a brand-new top-level group           |

```jsonc
{
  "name": "build-cloud",
  "description": "Build a component via the cloud.",
  "inputSchema": { /* ... */ },
  "_meta": {
    "azldev": {
      "command-path": ["component", "cloud-build"]
    }
  }
}
```

When `command-path` is honored, the tool appears as
`azldev component cloud-build`. When it isn't (collision with a built-in,
reserved root, leaf already taken, etc.), azldev logs a warning and
**falls back** to the namespace `azldev plugin <plugin-name> <tool-name>`.
This means a misconfigured `_meta` block never makes a tool unreachable —
it just lands somewhere predictable.

### Resolution rules

1. The first segment of `command-path` may not name a reserved root
   (`help`, `completion`, `version`, `advanced`, `plugin`).
2. Each non-leaf segment must either be an existing group node (built-in
   or contributed by another plugin) or a name that doesn't yet exist
   (in which case azldev creates it as a new group node).
3. The leaf segment must not collide with any sibling at the parent —
   built-in commands always win on collision; plugin-vs-plugin is
   first-come-wins.
4. Validation never mutates the Cobra tree on failure: a rejected graft
   leaves the tree as it was.

### Inspecting placements

`azldev advanced plugin list` shows one row per loaded plugin (name,
version, path, tool count, manifest status). `azldev advanced plugin
info <name>` shows full detail including the resolved destination of
each advertised tool, so users can confirm where a tool will appear in
help.

```sh
azldev --plugin ./azldev-plugin-hello -O json advanced plugin info hello
```

## Out of scope (still)

The current implementation deliberately omits:

- **TOML / XDG configuration.** Plugins must be supplied via `--plugin`;
  no `[plugins.<name>]` config section yet.
- **Plugin settings.** Plugins do not yet receive a configuration blob;
  they run with whatever args/env they spawn under.
- **Project-config injection.** Plugins cannot yet read the resolved
  project config.
- **Provider/backend registration.** Built-in commands (e.g.,
  `azldev component build`) cannot yet route to plugin tools via flags
  like `--builder=<name>`.
- **Caching of tool catalogs.** Every `azldev` invocation re-spawns each
  plugin to discover its tools.
- **Trust and integrity controls.** No `sha256` pin, no `trust =
  user/system` model. Trust is the OS file-permission model alone.

Each of these is on the roadmap; see the design notes in the project's
plan documents for sequencing.
