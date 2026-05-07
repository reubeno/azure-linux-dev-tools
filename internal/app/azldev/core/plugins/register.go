// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package plugins

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/cobra"
)

// CommandGroupID is the Cobra group ID assigned to the top-level 'plugin'
// command. It is registered with the root command on first use.
const CommandGroupID = "plugins"

// pluginGroupTitle is the human-readable title shown in the root command's
// help output for the plugin group.
const pluginGroupTitle = "Plugin commands:"

// pluginCommandUse is the Cobra `Use` string for the top-level plugin command
// node. The 'plugin' name is reserved for plugin-loader use.
const pluginCommandUse = "plugin"

// reservedTopLevelNames lists Cobra command names at the root that plugins
// must never collide with, in any phase. The MVP only enforces collisions
// against the literal 'plugin' parent (since that's where MVP places things);
// later phases will use this list for graft-collision checks.
//
//nolint:gochecknoglobals,unused // referenced by upcoming phases; keeping co-located reduces drift.
var reservedTopLevelNames = []string{
	"help",
	"completion",
	"version",
	"advanced",
	pluginCommandUse,
}

// Host is the small surface a plugin-host application must implement so the
// plugin loader can route tool output and surface failure-mode hints. The
// azldev application's [azldev.Env] satisfies this interface naturally; tests
// can supply a lightweight stub.
type Host interface {
	// OutputWriter is where successful tool textual results are written.
	// Typically the host's report file (defaults to os.Stdout).
	OutputWriter() io.Writer

	// AddFixSuggestion attaches an actionable suggestion to be displayed
	// after the command fails. Multiple calls accumulate.
	AddFixSuggestion(suggestion string)
}

// stdoutHost is the fallback Host used when the caller passes nil. It writes
// to os.Stdout and discards fix suggestions. Useful in unit tests and as a
// safety net.
type stdoutHost struct{}

func (stdoutHost) OutputWriter() io.Writer   { return os.Stdout }
func (stdoutHost) AddFixSuggestion(_ string) {}

// Register adds a top-level 'plugin' command group to root and, for each
// supplied plugin, registers one Cobra subcommand per advertised tool under
// 'plugin <plugin-name> <tool-name>'.
//
// Tools whose input schema is not representable in MVP (see [addToolFlags])
// are skipped with a warning; other tools from the same plugin remain
// usable. If two plugins report the same canonical name, registration fails:
// the user is asked to disambiguate by choosing different binaries.
//
// Register is a no-op (and adds no group) when the plugin slice is empty.
//
// Pass a non-nil [Host] to integrate output and fix-suggestion routing with
// the host application; pass nil to fall back to a writes-to-stdout host.
func Register(root *cobra.Command, host Host, loaded []*Plugin) error {
	if len(loaded) == 0 {
		return nil
	}

	if host == nil {
		host = stdoutHost{}
	}

	if err := assertNoCollidingPluginNames(loaded); err != nil {
		return err
	}

	if err := assertNoTopLevelCollision(root); err != nil {
		return err
	}

	parent := newPluginParentCommand()

	if !rootHasGroup(root, CommandGroupID) {
		root.AddGroup(&cobra.Group{
			ID:    CommandGroupID,
			Title: pluginGroupTitle,
		})
	}

	parent.GroupID = CommandGroupID

	for _, plugin := range loaded {
		parent.AddCommand(newPluginCommand(host, plugin))
	}

	root.AddCommand(parent)

	return nil
}

// newPluginParentCommand constructs the top-level 'plugin' command node. It
// is just a documentation/grouping node; it has no RunE of its own.
func newPluginParentCommand() *cobra.Command {
	return &cobra.Command{
		Use:   pluginCommandUse,
		Short: "Run commands provided by loaded plugins",
		Long: `Run commands provided by plugins loaded via the '--plugin' flag.

Each loaded plugin appears as a subcommand here, and each tool the plugin
advertises appears as a leaf under that subcommand. Use '--help' on any of
them for tool-specific usage information.`,
	}
}

// newPluginCommand constructs the per-plugin namespace command (the middle
// node in 'plugin <plugin-name> <tool-name>'). Tool subcommands are added as
// children.
func newPluginCommand(host Host, plugin *Plugin) *cobra.Command {
	short := fmt.Sprintf("Tools from plugin %#q", plugin.Name())
	if plugin.Version() != "" {
		short = fmt.Sprintf("Tools from plugin %#q (v%s)", plugin.Name(), plugin.Version())
	}

	cmd := &cobra.Command{
		Use:   plugin.Name(),
		Short: short,
	}

	for _, tool := range plugin.Tools() {
		toolCmd, err := newToolCommand(host, plugin, tool)
		if err != nil {
			slog.Warn("skipping plugin tool with unsupported input schema",
				"plugin", plugin.Name(),
				"tool", tool.Name,
				"err", err,
			)

			continue
		}

		cmd.AddCommand(toolCmd)
	}

	return cmd
}

// newToolCommand constructs the leaf Cobra command for a single MCP tool.
// Returns [ErrUnsupportedSchema] (wrapped) if any property in the tool's
// input schema can't be represented as a Cobra flag.
func newToolCommand(host Host, plugin *Plugin, tool mcp.Tool) (*cobra.Command, error) {
	cmd := &cobra.Command{
		Use:   tool.Name,
		Short: firstLineOf(tool.Description),
		Long:  tool.Description,
		// Tool commands don't accept positional args in MVP; cobra rejects
		// extras at parse time with a usage hint.
		Args: cobra.NoArgs,
	}

	if err := addToolFlags(cmd, tool); err != nil {
		return nil, err
	}

	cmd.RunE = makeToolRunE(host, plugin, tool)

	return cmd, nil
}

// makeToolRunE returns a Cobra RunE function that collects flag values,
// invokes the tool, and prints the textual result via the host's report
// writer.
func makeToolRunE(host Host, plugin *Plugin, tool mcp.Tool) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		// At this point we've passed argument parsing; any further error is
		// not a usage error and shouldn't trigger Cobra usage output.
		cmd.SilenceUsage = true

		toolArgs, err := collectFlagValues(cmd, tool)
		if err != nil {
			return err
		}

		text, err := plugin.Call(cmd.Context(), tool.Name, toolArgs)

		// Always emit any captured text — many tools emit a partial diagnostic
		// even on error and that's useful to the user. The error itself is
		// returned to Cobra below.
		if text != "" {
			_, _ = fmt.Fprintln(host.OutputWriter(), text)
		}

		if err != nil {
			addToolFixSuggestion(host, plugin, err)

			return err
		}

		return nil
	}
}

// addToolFixSuggestion attaches a category-specific fix suggestion to the
// host so the user gets actionable next-step text after a plugin-related
// failure.
func addToolFixSuggestion(host Host, plugin *Plugin, err error) {
	switch {
	case errors.Is(err, ErrPluginCall):
		host.AddFixSuggestion(fmt.Sprintf(
			"The plugin process at %#q may have crashed or stopped responding. "+
				"Re-run with '--verbose' to see plugin stderr output.",
			plugin.Path()))
	case errors.Is(err, ErrPluginToolError):
		host.AddFixSuggestion(fmt.Sprintf(
			"The plugin tool reported an error. Refer to the message above; "+
				"plugin %#q at %#q may need different arguments or external state.",
			plugin.Name(), plugin.Path()))
	}
}

// assertNoCollidingPluginNames returns an error if any two plugins in loaded
// share the same canonical name. We refuse rather than silently overwrite so
// that users notice and can disambiguate.
func assertNoCollidingPluginNames(loaded []*Plugin) error {
	seen := make(map[string]string, len(loaded))

	for _, plugin := range loaded {
		if existing, ok := seen[plugin.Name()]; ok {
			return fmt.Errorf(
				"plugin name collision: plugins at %#q and %#q both report name %#q; "+
					"distinct plugins must have distinct serverInfo names",
				existing, plugin.Path(), plugin.Name())
		}

		seen[plugin.Name()] = plugin.Path()
	}

	return nil
}

// assertNoTopLevelCollision returns an error if a built-in command at the
// root already uses the reserved 'plugin' name. This is a structural check
// against accidental future regressions in azldev itself; a built-in
// 'plugin' command would shadow the loader's parent node.
func assertNoTopLevelCollision(root *cobra.Command) error {
	for _, child := range root.Commands() {
		if child.Name() == pluginCommandUse {
			return fmt.Errorf(
				"top-level command %#q is reserved for plugin loading; "+
					"a built-in command of the same name was registered",
				pluginCommandUse)
		}
	}

	return nil
}

// rootHasGroup reports whether root already has a Cobra group with id, so we
// don't double-register on repeat calls.
func rootHasGroup(root *cobra.Command, id string) bool {
	for _, group := range root.Groups() {
		if group.ID == id {
			return true
		}
	}

	return false
}

// firstLineOf returns the first non-empty line of text. Cobra renders Short
// in list views and we want to keep multi-line tool descriptions readable.
func firstLineOf(text string) string {
	for i := range len(text) {
		if text[i] == '\n' {
			return text[:i]
		}
	}

	return text
}
