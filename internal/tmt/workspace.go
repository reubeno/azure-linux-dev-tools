// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tmt

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
)

// TMT rsyncs its absolute native workdir to SSH guests. A short, conventional
// temporary path works for non-root guests and avoids SSH socket path limits.
func nativeWorkspace(ctx opctx.Ctx) (string, error) {
	const root = "/var/tmp"
	if err := fileutils.MkdirAll(ctx.FS(), root); err != nil {
		return "", fmt.Errorf("create native TMT workspace root:\n%w", err)
	}

	path, err := fileutils.MkdirTemp(ctx.FS(), root, "azlt-")
	if err != nil {
		return "", fmt.Errorf("create native TMT workspace:\n%w", err)
	}

	return path, nil
}

func (r *Runner) executeAndCollect(ctx opctx.Ctx, request testframework.RunRequest,
	sourceDir, program, pluginDir, outputDir string,
) (status testframework.Status, err error) {
	root, err := nativeWorkspace(ctx)
	if err != nil {
		return testframework.Errored, err
	}

	nativeRun := filepath.Join(root, "run")

	defer func() {
		if cleanupErr := ctx.FS().RemoveAll(root); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove native TMT workspace:\n%w", cleanupErr))
			status = testframework.Errored
		}
	}()

	args, err := r.prepareArguments(ctx, request, sourceDir, program, nativeRun)
	if err != nil {
		return testframework.Errored, err
	}

	status, runErr := runTMTCommand(ctx, sourceDir, pluginDir, program, request.Execution.Strategy, args...)
	// Cleanup must precede removal of the private Python environment.
	managedGuest := request.Execution.Strategy == testframework.VM || request.Execution.Strategy == testframework.Container
	if runErr != nil && managedGuest {
		if cleanupErr := cleanGuests(ctx, program, nativeRun, pluginDir, request.Execution.Strategy); cleanupErr != nil {
			runErr = errors.Join(runErr, cleanupErr)
			status = testframework.Errored
		}
	}

	exists, statErr := fileutils.DirExists(ctx.FS(), nativeRun)
	if statErr != nil {
		return testframework.Errored, errors.Join(runErr, fmt.Errorf("inspect TMT run state:\n%w", statErr))
	}

	if exists {
		copyCtx := opctx.WithContext(ctx, context.WithoutCancel(ctx))
		if copyErr := fileutils.CopyDirRecursive(copyCtx, ctx.FS(), nativeRun, outputDir,
			fileutils.CopyDirOptions{
				CopyFileOptions: fileutils.CopyFileOptions{PreserveFileMode: true},
				FileFilter: func(_ opctx.FS, destination string) (bool, error) {
					relative, err := filepath.Rel(outputDir, destination)
					if err != nil {
						return false, fmt.Errorf("resolve collected TMT path:\n%w", err)
					}
					// Interrupted SSH masters can leave sockets; these are runtime state, not artifacts.
					return !strings.HasPrefix(relative, "ssh-sockets"+string(filepath.Separator)), nil
				},
			}); copyErr != nil {
			return testframework.Errored, errors.Join(runErr, fmt.Errorf("collect TMT run state:\n%w", copyErr))
		}
	}

	return status, runErr
}

func cleanGuests(ctx opctx.Ctx, program, nativeRun, pluginDir string, strategy testframework.Strategy) error {
	exists, err := fileutils.DirExists(ctx.FS(), nativeRun)
	if err != nil {
		return fmt.Errorf("inspect failed TMT run:\n%w", err)
	}

	if !exists {
		return nil
	}

	const cleanupTimeout = 2 * time.Minute

	lifetime, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()

	cleanupCtx := opctx.WithContext(ctx, lifetime)
	raw := exec.CommandContext(lifetime, program, "clean", "guests",
		"--workdir-root", filepath.Dir(nativeRun), "--id", nativeRun)
	raw.Env = append(raw.Environ(), commandEnv(ctx, pluginDir, strategy)...)

	var stderr strings.Builder

	raw.Stderr = &stderr

	cmd, err := cleanupCtx.Command(raw)
	if err != nil {
		return fmt.Errorf("create scoped TMT cleanup:\n%w", err)
	}

	if err := cmd.Run(cleanupCtx); err != nil {
		return fmt.Errorf("clean guests for TMT run %#q (%s):\n%w", nativeRun, strings.TrimSpace(stderr.String()), err)
	}

	return nil
}
