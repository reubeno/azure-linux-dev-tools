// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package plugins

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// LoadAll spawns each plugin binary listed in paths and returns the live
// handles. On failure, all plugins successfully spawned before the failure
// are closed before the error is returned, so callers do not leak
// subprocesses.
//
// The MVP is fail-fast: if any single plugin can't be spawned, initialized
// or queried for tools, the function returns the wrapped error and no
// plugins remain live. This is appropriate for v1 since plugins are
// explicitly named on the command line — a typo or stale path should be
// loud, not silent.
func LoadAll(ctx context.Context, paths []string) (loaded []*Plugin, err error) {
	loaded = make([]*Plugin, 0, len(paths))

	defer func() {
		// On any error, tear down anything we already spawned so the caller
		// has nothing to close on the failure path.
		if err != nil {
			_ = closeAll(loaded)
			loaded = nil
		}
	}()

	for _, path := range paths {
		p, spawnErr := Spawn(ctx, path, nil, nil)
		if spawnErr != nil {
			return nil, spawnErr
		}

		loaded = append(loaded, p)
	}

	return loaded, nil
}

// CloseAll shuts down every Plugin in the slice, returning a joined error
// containing any individual close failures (or nil if all succeeded). Always
// attempts every Close even after one returns an error, so all subprocesses
// are torn down regardless.
func CloseAll(loaded []*Plugin) error {
	return closeAll(loaded)
}

func closeAll(loaded []*Plugin) error {
	var errs []error

	for _, plugin := range loaded {
		if plugin == nil {
			continue
		}

		if cerr := plugin.Close(); cerr != nil {
			errs = append(errs, cerr)

			slog.Warn("error closing plugin",
				"plugin", plugin.Name(),
				"path", plugin.Path(),
				"err", cerr,
			)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to close one or more plugins:\n%w", errors.Join(errs...))
	}

	return nil
}
