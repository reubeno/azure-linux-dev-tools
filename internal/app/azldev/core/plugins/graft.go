// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package plugins

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

// AnnotationPluginIntroducedBy is set on Cobra group nodes that azldev
// creates on behalf of a plugin during grafting. The value is the canonical
// plugin name. Subsequent plugins contributing to the same group inherit
// it; a different group-title hint produces a warning rather than an error.
const AnnotationPluginIntroducedBy = "azldev.plugin-introduced-by"

// errGraftPathReserved is returned by [tryGraft] when the requested
// command-path begins with a reserved top-level name. Callers map this to
// a fallback-to-namespace decision and a single warning line.
var errGraftPathReserved = errors.New("reserved top-level name")

// errGraftPathBlocked is returned when an intermediate segment of the
// requested command-path already exists but is not a group node (it has a
// Run/RunE handler), so adding children there would be incorrect.
var errGraftPathBlocked = errors.New("path segment is a leaf command")

// errGraftLeafCollision is returned when the leaf segment of the requested
// command-path already exists at its parent (regardless of whether the
// existing command is built-in or contributed by an earlier plugin).
var errGraftLeafCollision = errors.New("command name already taken at this path")

// reservedTopLevelSet is the set of [reservedTopLevelNames] in lookup form.
//
//nolint:gochecknoglobals // effectively constant; computed from reservedTopLevelNames at init.
var reservedTopLevelSet = func() map[string]struct{} {
	out := make(map[string]struct{}, len(reservedTopLevelNames))
	for _, name := range reservedTopLevelNames {
		out[name] = struct{}{}
	}

	return out
}()

// tryGraft attempts to attach cmd to root at the path described by hints.
// On success the leaf command is placed in the Cobra tree; on failure an
// error is returned and the caller is expected to install cmd in the
// fallback 'plugin <plugin-name> <tool-name>' namespace instead.
//
// tryGraft never mutates root on failure: validation runs to completion
// before any new groups are created or the leaf is attached.
func tryGraft(root *cobra.Command, cmd *cobra.Command, plugin *Plugin, hints *ToolGraftHints) error {
	if hints == nil || len(hints.CommandPath) == 0 {
		return errors.New("no command-path specified")
	}

	path := hints.CommandPath

	if _, reserved := reservedTopLevelSet[path[0]]; reserved {
		return fmt.Errorf("%w: %#q", errGraftPathReserved, path[0])
	}

	parentSegments := path[:len(path)-1]
	leafName := path[len(path)-1]

	// Pre-flight: walk parentSegments to verify each is either an existing
	// group or a name we can create as a new one. We don't mutate the tree
	// in this pre-flight; instead we record which segments need to be
	// created on the way down so the actual mutation can happen
	// atomically once we've also confirmed the leaf isn't taken.
	parent, segmentsToCreate, err := preflightGraftPath(root, parentSegments)
	if err != nil {
		return err
	}

	// Determine the final parent we'd attach to, taking newly-created
	// segments into account, so we can leaf-collision-check accurately.
	terminalParent := terminalParent(parent, segmentsToCreate)
	if hasChildNamed(terminalParent, leafName) {
		return fmt.Errorf("%w: %#q at %v", errGraftLeafCollision, leafName, path)
	}

	// Mutation phase. Create new group nodes (oldest-to-newest), then
	// attach the leaf. The group-title hint applies only to the deepest
	// freshly-introduced segment.
	cursor := parent

	for index, segment := range segmentsToCreate {
		group := &cobra.Command{
			Use: segment,
			Annotations: map[string]string{
				AnnotationPluginIntroducedBy: plugin.Name(),
			},
		}

		// Surface the title only on the deepest newly-created segment when
		// a group-title hint is provided; intermediate segments inherit a
		// blank short. This keeps multi-segment grafts deterministic.
		if index == len(segmentsToCreate)-1 && hints.GroupTitle != "" {
			group.Short = hints.GroupTitle
		}

		// Top-level plugin-introduced groups go under the plugin command
		// group ID so they list together in '--help' rather than
		// scattering across "Additional Commands".
		if cursor == root {
			ensurePluginsGroup(root)

			group.GroupID = CommandGroupID
		}

		cursor.AddCommand(group)
		cursor = group
	}

	cmd.Use = leafName
	if hints.Hidden {
		cmd.Hidden = true
	}

	if len(hints.Aliases) > 0 {
		cmd.Aliases = hints.Aliases
	}

	cursor.AddCommand(cmd)

	return nil
}

// preflightGraftPath walks parentSegments under root, returning the
// deepest existing ancestor and the (possibly empty) suffix of segments
// that would need to be created beneath it. Returns an error if any
// existing segment is a leaf command (cannot accept children).
func preflightGraftPath(root *cobra.Command, parentSegments []string) (*cobra.Command, []string, error) {
	cursor := root

	for index, segment := range parentSegments {
		child := findChild(cursor, segment)
		if child == nil {
			// First missing segment marks where new-group creation begins.
			return cursor, parentSegments[index:], nil
		}

		if isLeafCommand(child) {
			return nil, nil, fmt.Errorf(
				"%w: %#q in path is a callable command, not a group", errGraftPathBlocked, segment)
		}

		cursor = child
	}

	return cursor, nil, nil
}

// terminalParent returns the cobra.Command that the leaf will be attached
// to once segmentsToCreate (if any) have been added under existingParent.
// During pre-flight we don't actually create those nodes, so we compute
// the would-be terminal by treating the segment chain as virtual: the
// terminal is the existingParent if no segments need creating, or the
// final newly-created segment otherwise.
//
// The actual collision check still has to be done against existingParent
// for the first newly-created segment (the new group can't already exist
// there). For deeper newly-created segments, by definition they don't
// exist yet, so leaf collisions there are impossible.
func terminalParent(existingParent *cobra.Command, segmentsToCreate []string) *cobra.Command {
	if len(segmentsToCreate) == 0 {
		return existingParent
	}

	// Brand-new chain — nothing can collide on the leaf below it. The
	// "terminal parent" is the last new group, which we represent here as
	// nil; callers must interpret nil as "no existing children to clash
	// with".
	return nil
}

// hasChildNamed reports whether parent (which may be nil, see
// [terminalParent]) already has a child with the given name.
func hasChildNamed(parent *cobra.Command, name string) bool {
	if parent == nil {
		return false
	}

	return findChild(parent, name) != nil
}

// findChild returns the immediate child of parent whose Name() equals
// name, or nil if no such child exists. Names are matched against
// [cobra.Command.Name], which strips the trailing flag spec from Use.
func findChild(parent *cobra.Command, name string) *cobra.Command {
	for _, child := range parent.Commands() {
		if child.Name() == name {
			return child
		}
	}

	return nil
}

// isLeafCommand reports whether cmd is a callable command (has Run or RunE
// installed). Group nodes are pure containers and have neither.
func isLeafCommand(cmd *cobra.Command) bool {
	return cmd.Run != nil || cmd.RunE != nil
}

// ensurePluginsGroup adds the plugin Cobra group to root if it isn't there
// yet. Called both for top-level plugin-introduced groups and for the
// fallback parent so they list together in root help.
func ensurePluginsGroup(root *cobra.Command) {
	if rootHasGroup(root, CommandGroupID) {
		return
	}

	root.AddGroup(&cobra.Group{
		ID:    CommandGroupID,
		Title: pluginGroupTitle,
	})
}
