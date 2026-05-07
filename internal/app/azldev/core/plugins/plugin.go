// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package plugins implements the azldev side of the MCP-stdio plugin protocol.
//
// In MVP scope, an azldev plugin is any executable that speaks the Model
// Context Protocol over its standard I/O. azldev spawns the plugin as a
// subprocess, performs the MCP handshake, lists the tools it advertises, and
// registers each tool as a Cobra subcommand under the namespace
// 'azldev plugin <plugin-name> <tool-name>'.
//
// The MVP intentionally does not yet honor any vendor-extension metadata
// (such as '_meta.azldev' on individual tools) — every tool is reachable
// only via the canonical namespace. Later phases will read manifest
// resources and per-tool annotations to graft commands elsewhere in the
// Cobra hierarchy and to register named providers.
package plugins

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// Sentinel errors describing the MVP failure categories. Each invocation site
// wraps one of these to give callers a simple way to discriminate spawn,
// handshake, discovery, transport and tool-error failures via [errors.Is].
var (
	// ErrPluginSpawn indicates that the plugin subprocess could not be started.
	ErrPluginSpawn = errors.New("plugin spawn failed")
	// ErrPluginInitialize indicates that the MCP handshake with the plugin failed.
	ErrPluginInitialize = errors.New("plugin handshake failed")
	// ErrPluginListTools indicates that the plugin's tool catalog could not be retrieved.
	ErrPluginListTools = errors.New("plugin tool discovery failed")
	// ErrPluginCall indicates a transport/protocol error while invoking a plugin tool.
	ErrPluginCall = errors.New("plugin call failed")
	// ErrPluginToolError indicates that a plugin tool ran but reported an error result.
	ErrPluginToolError = errors.New("plugin tool reported an error")
)

// clientInfo identifies azldev to plugins it spawns. The version string is
// intentionally generic — plugins should not branch on the host version.
//
//nolint:gochecknoglobals // effectively constant; Go doesn't allow struct consts.
var clientInfo = mcp.Implementation{
	Name:    "azldev",
	Version: "1",
}

// Plugin represents a single live MCP plugin subprocess and its discovered
// tool catalog. A Plugin is created via [Spawn] and must be released with
// [Plugin.Close].
type Plugin struct {
	// path is the binary path the plugin was spawned from (used in errors and logs).
	path string
	// name is the canonical plugin name reported via MCP serverInfo.
	name string
	// version is the plugin version reported via MCP serverInfo.
	version string
	// client is the underlying mcp-go client; nil after Close.
	client *client.Client
	// tools is the catalog returned by tools/list during Spawn.
	tools []mcp.Tool
	// manifest is the parsed azldev://manifest resource, or nil when the
	// plugin doesn't publish one.
	manifest *Manifest

	// closeOnce ensures Close is idempotent.
	closeOnce sync.Once
}

// Spawn launches the given binary as an MCP server subprocess, completes the
// initialize handshake, and retrieves the tool catalog. The returned Plugin
// owns the live MCP session; the caller must invoke [Plugin.Close] when
// finished.
//
// The spawned subprocess inherits no environment by default; callers that
// need to forward variables must pass them explicitly via env.
func Spawn(ctx context.Context, path string, args []string, env []string) (plugin *Plugin, err error) {
	mcpClient, err := client.NewStdioMCPClientWithOptions(path, env, args)
	if err != nil {
		return nil, fmt.Errorf("%w at %#q:\n%w", ErrPluginSpawn, path, err)
	}

	// On any failure beyond this point, ensure the subprocess is torn down so
	// we don't leak processes when one of many plugins fails to come up.
	defer func() {
		if err != nil {
			_ = mcpClient.Close()
		}
	}()

	// Capture the plugin's stderr stream and route it into slog at debug
	// level under a per-plugin prefix. This runs for the lifetime of the
	// client.
	if stderr, ok := client.GetStderr(mcpClient); ok && stderr != nil {
		go forwardPluginStderr(path, stderr)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = clientInfo

	initResult, err := mcpClient.Initialize(ctx, initReq)
	if err != nil {
		return nil, fmt.Errorf("%w at %#q:\n%w", ErrPluginInitialize, path, err)
	}

	// Capture canonical plugin name. Fall back to the binary path if the
	// plugin didn't supply a name, so we can still register commands.
	name := initResult.ServerInfo.Name
	if name == "" {
		name = path
	}

	listResult, err := mcpClient.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("%w from %#q:\n%w", ErrPluginListTools, name, err)
	}

	manifest, err := readManifest(ctx, mcpClient)
	if err != nil {
		// A malformed/version-mismatched manifest is a hard error. We
		// surface it via the handshake category since it's effectively a
		// negotiation problem between azldev and the plugin.
		return nil, fmt.Errorf("%w at %#q:\n%w", ErrPluginInitialize, path, err)
	}

	slog.Debug(
		"loaded plugin",
		"plugin", name,
		"version", initResult.ServerInfo.Version,
		"path", path,
		"tools", len(listResult.Tools),
		"has-manifest", manifest != nil,
	)

	return &Plugin{
		path:     path,
		name:     name,
		version:  initResult.ServerInfo.Version,
		client:   mcpClient,
		tools:    listResult.Tools,
		manifest: manifest,
	}, nil
}

// Name returns the canonical plugin name reported by the MCP server during
// initialization (or the binary path as a fallback if the plugin didn't
// provide one).
func (p *Plugin) Name() string {
	return p.name
}

// Path returns the binary path the plugin was spawned from.
func (p *Plugin) Path() string {
	return p.path
}

// Version returns the plugin version reported via MCP serverInfo, or an
// empty string if the plugin didn't report one.
func (p *Plugin) Version() string {
	return p.version
}

// Tools returns the catalog of MCP tools advertised by the plugin. The slice
// is owned by the Plugin; callers must not mutate it.
func (p *Plugin) Tools() []mcp.Tool {
	return p.tools
}

// Manifest returns the parsed plugin-level manifest (the contents of the
// 'azldev://manifest' MCP resource), or nil when the plugin does not
// publish one.
func (p *Plugin) Manifest() *Manifest {
	return p.manifest
}

// Call invokes the named tool on the plugin and returns the concatenated
// text content of the result.
//
// Errors are wrapped with [ErrPluginCall] for transport/protocol failures and
// [ErrPluginToolError] when the tool itself reported an error result.
func (p *Plugin) Call(ctx context.Context, toolName string, args map[string]any) (text string, err error) {
	if p.client == nil {
		return "", fmt.Errorf("%w: plugin %#q already closed", ErrPluginCall, p.name)
	}

	req := mcp.CallToolRequest{}
	req.Params.Name = toolName
	req.Params.Arguments = args

	result, err := p.client.CallTool(ctx, req)
	if err != nil {
		return "", fmt.Errorf("%w: plugin %#q tool %#q:\n%w",
			ErrPluginCall, p.name, toolName, err)
	}

	text = collectText(result.Content)

	if result.IsError {
		// Surface the tool's own error message verbatim if it provided one.
		msg := text
		if msg == "" {
			msg = "(no message)"
		}

		return text, fmt.Errorf("%w: plugin %#q tool %#q: %s",
			ErrPluginToolError, p.name, toolName, msg)
	}

	return text, nil
}

// Close shuts down the underlying MCP client and waits for the subprocess to
// exit. It is safe to call Close more than once; subsequent calls are no-ops.
func (p *Plugin) Close() error {
	var err error

	p.closeOnce.Do(func() {
		if p.client != nil {
			err = p.client.Close()
			p.client = nil
		}
	})

	if err != nil {
		return fmt.Errorf("failed to close plugin %#q:\n%w", p.name, err)
	}

	return nil
}

// collectText concatenates the text bodies of all [mcp.TextContent] entries in
// the result, separated by newlines. Non-text content types are ignored in
// the MVP; later phases may surface them differently.
func collectText(contents []mcp.Content) string {
	var out []byte

	for _, c := range contents {
		text, ok := c.(mcp.TextContent)
		if !ok {
			continue
		}

		if len(out) > 0 {
			out = append(out, '\n')
		}

		out = append(out, text.Text...)
	}

	return string(out)
}

// pluginStderrBufLimit caps individual stderr lines from plugins so a
// runaway plugin can't OOM us. Lines longer than this are silently
// truncated by [bufio.Scanner].
const pluginStderrBufLimit = 1024 * 1024

// pluginStderrBufInitial is the initial scanner buffer size; the scanner
// grows it on demand up to [pluginStderrBufLimit].
const pluginStderrBufInitial = 64 * 1024

// forwardPluginStderr streams the plugin's stderr output into the host's
// structured logger at debug level. Each line is logged independently so it
// blends with other slog output rather than appearing as one huge blob at
// shutdown time.
func forwardPluginStderr(path string, stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, pluginStderrBufInitial), pluginStderrBufLimit)

	for scanner.Scan() {
		slog.Debug("plugin stderr", "path", path, "line", scanner.Text())
	}
}
