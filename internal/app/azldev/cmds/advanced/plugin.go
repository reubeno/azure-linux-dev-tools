// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package advanced

import (
	"errors"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/plugins"
	"github.com/spf13/cobra"
)

// pluginOnAppInit registers the 'azldev advanced plugin' command tree on
// app init. The tree is a parent group with 'list' and 'info' subcommands
// that introspect the plugins loaded via the global '--plugin' flag.
func pluginOnAppInit(_ *azldev.App, parentCmd *cobra.Command) {
	pluginCmd := &cobra.Command{
		Use:   "plugin",
		Short: "Inspect plugins loaded for this azldev invocation",
		Long: `Inspect plugins loaded for this azldev invocation via '--plugin'.

These commands are read-only and never spawn additional plugin processes;
they introspect the live handles owned by the current azldev invocation.`,
	}

	pluginCmd.AddCommand(newPluginListCmd())
	pluginCmd.AddCommand(newPluginInfoCmd())

	parentCmd.AddCommand(pluginCmd)
}

// PluginInfo is the table-friendly summary row returned by
// 'azldev advanced plugin list'. Each loaded plugin contributes one row.
type PluginInfo struct {
	Name          string
	Version       string
	Path          string
	Tools         int
	HasManifest   bool
	ManifestTitle string
	Providers     int
}

// PluginDetail is the full per-plugin record returned by
// 'azldev advanced plugin info <name>'. It includes the tool catalog and
// (when present) the parsed manifest fields, so users can confirm what
// azldev sees about a plugin without having to inspect MCP traffic.
type PluginDetail struct {
	Name      string
	Version   string
	Path      string
	Manifest  *plugins.Manifest
	Tools     []PluginToolDetail
	Providers []PluginProviderDetail
}

// PluginToolDetail is one row of [PluginDetail.Tools]; it surfaces the tool
// name, description, and the resolved graft destination so users can
// quickly answer "where does each tool show up in --help?".
type PluginToolDetail struct {
	Name        string
	Description string
	Destination string
}

// PluginProviderDetail summarizes one provider registration. It mirrors
// [plugins.ProviderRef] for output-format compatibility but lives here so
// it can vary in formatting independently from the wire shape.
type PluginProviderDetail struct {
	Kind string
	Name string
	Tool string
}

// newPluginListCmd returns 'azldev advanced plugin list', which produces
// one row of [PluginInfo] per loaded plugin in deterministic name order.
func newPluginListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List plugins loaded for this azldev invocation",
		RunE: azldev.RunFuncWithoutRequiredConfig(func(env *azldev.Env) (interface{}, error) {
			loaded := env.LoadedPlugins()

			rows := make([]PluginInfo, 0, len(loaded))
			for _, plugin := range loaded {
				rows = append(rows, summarizePlugin(plugin))
			}

			return rows, nil
		}),
	}
}

// newPluginInfoCmd returns 'azldev advanced plugin info <name>', which
// renders the full [PluginDetail] for one plugin (looked up by canonical
// name as reported via MCP serverInfo).
func newPluginInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info <plugin-name>",
		Short: "Show full detail for one loaded plugin",
		Args:  cobra.ExactArgs(1),
		RunE: azldev.RunFuncWithoutRequiredConfigWithExtraArgs(
			func(env *azldev.Env, args []string) (interface{}, error) {
				plugin, err := findPluginByName(env.LoadedPlugins(), args[0])
				if err != nil {
					return nil, err
				}

				return detailPlugin(plugin), nil
			},
		),
	}
}

// summarizePlugin converts a live [plugins.Plugin] into the table row used
// by 'plugin list'.
func summarizePlugin(plugin *plugins.Plugin) PluginInfo {
	info := PluginInfo{
		Name:    plugin.Name(),
		Version: plugin.Version(),
		Path:    plugin.Path(),
		Tools:   len(plugin.Tools()),
	}

	if manifest := plugin.Manifest(); manifest != nil {
		info.HasManifest = true
		info.ManifestTitle = manifest.Title
		info.Providers = len(manifest.Providers)
	}

	return info
}

// detailPlugin builds the structured detail record returned by 'plugin
// info'. Per-tool destinations are resolved by examining each tool's
// '_meta.azldev' hints; tools without hints (or with rejected hints)
// surface as the namespace path.
func detailPlugin(plugin *plugins.Plugin) PluginDetail {
	tools := make([]PluginToolDetail, 0, len(plugin.Tools()))
	for _, tool := range plugin.Tools() {
		tools = append(tools, PluginToolDetail{
			Name:        tool.Name,
			Description: firstLine(tool.Description),
			Destination: resolveToolDestination(plugin, tool.Name),
		})
	}

	var providers []PluginProviderDetail

	if manifest := plugin.Manifest(); manifest != nil {
		providers = make([]PluginProviderDetail, 0, len(manifest.Providers))
		for _, ref := range manifest.Providers {
			providers = append(providers, PluginProviderDetail{
				Kind: ref.Kind,
				Name: ref.Name,
				Tool: ref.Tool,
			})
		}
	}

	return PluginDetail{
		Name:      plugin.Name(),
		Version:   plugin.Version(),
		Path:      plugin.Path(),
		Manifest:  plugin.Manifest(),
		Tools:     tools,
		Providers: providers,
	}
}

// resolveToolDestination returns a human-readable rendering of where a
// plugin tool appears in the Cobra hierarchy. We re-derive it from the
// tool's metadata rather than walking the live tree so the output stays
// stable even if other plugins later mutate sibling commands.
func resolveToolDestination(plugin *plugins.Plugin, toolName string) string {
	tool, found := lookupTool(plugin, toolName)
	if !found {
		return ""
	}

	hints, _ := plugins.ParseToolGraftHints(tool)
	if hints != nil && len(hints.CommandPath) > 0 {
		return "azldev " + strings.Join(hints.CommandPath, " ")
	}

	return fmt.Sprintf("azldev plugin %s %s", plugin.Name(), toolName)
}

// lookupTool finds a tool by name within a plugin's catalog. Returns
// ok=false when no such tool exists.
func lookupTool(plugin *plugins.Plugin, toolName string) (mcp.Tool, bool) {
	for _, tool := range plugin.Tools() {
		if tool.Name == toolName {
			return tool, true
		}
	}

	return mcp.Tool{}, false
}

// findPluginByName picks the plugin with the given canonical name out of
// the loaded set. We canonicalize this lookup here so error messages match
// across 'list' and 'info'.
func findPluginByName(loaded []*plugins.Plugin, name string) (*plugins.Plugin, error) {
	for _, plugin := range loaded {
		if plugin.Name() == name {
			return plugin, nil
		}
	}

	if len(loaded) == 0 {
		return nil, errors.New("no plugins are loaded; use the global '--plugin' flag to register one")
	}

	available := make([]string, 0, len(loaded))
	for _, plugin := range loaded {
		available = append(available, plugin.Name())
	}

	return nil, fmt.Errorf("plugin %#q is not loaded; available: %s",
		name, strings.Join(available, ", "))
}

// firstLine returns the first line of a (possibly multi-line) string. We
// keep tool descriptions terse in tabular output.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}

	return s
}
