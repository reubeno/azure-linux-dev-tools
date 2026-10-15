// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tmt_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/tmt"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScopedCleanupRetainsDiagnostics(t *testing.T) {
	ctx := newRunContext(t)
	runner, err := tmt.New("failed", nil)
	require.NoError(t, err)

	cleanupErr := errors.New("cleanup failed")
	ctx.CmdFactory.RunHandler = func(cmd *exec.Cmd) error {
		if filepath.Base(cmd.Args[0]) != tmtProgram {
			return nil
		}

		if slices.Contains(cmd.Args, "clean") {
			_, err := cmd.Stderr.Write([]byte("native cleanup diagnostic"))
			require.NoError(t, err)

			return cleanupErr
		}

		return errors.New("native execution failed")
	}
	_, err = runner.Run(ctx, requestFor(testframework.Container))
	require.ErrorIs(t, err, cleanupErr)
	require.ErrorContains(t, err, "native cleanup diagnostic")
}

func TestCancellationAllowsUnwindAndScopesFallbackCleanup(t *testing.T) {
	for _, strategy := range []testframework.Strategy{testframework.VM, testframework.Container, testframework.SSH} {
		t.Run(string(strategy), func(t *testing.T) {
			ctx := newRunContext(t)

			lifetime, cancel := context.WithCancel(t.Context())
			defer cancel()

			ctx.Ctx = lifetime
			runner, err := tmt.New("cancelled", nil)
			require.NoError(t, err)

			var (
				runID   string
				cleaned bool
			)

			ctx.CmdFactory.RunHandler = func(cmd *exec.Cmd) error {
				if filepath.Base(cmd.Args[0]) != tmtProgram {
					return nil
				}

				if slices.Contains(cmd.Args, "clean") {
					assert.Equal(t, []string{
						"clean", "guests", "--workdir-root", filepath.Dir(runID), "--id", runID,
					}, cmd.Args[1:])

					cleaned = true

					return nil
				}

				assert.NotNil(t, cmd.Cancel)
				require.ErrorIs(t, cmd.Cancel(), os.ErrProcessDone, "an unstarted process needs no signal")
				assert.Equal(t, time.Minute, cmd.WaitDelay)
				runID = cmd.Args[slices.Index(cmd.Args, "--id")+1]
				require.NoError(t, fileutils.MkdirAll(ctx.FS(), filepath.Join(runID, "ssh-sockets")))
				require.NoError(t, fileutils.WriteFile(ctx.FS(), filepath.Join(runID, "ssh-sockets", "guest.socket"),
					[]byte("runtime socket placeholder"), fileperms.PrivateFile))

				cancel()

				return context.Canceled
			}
			result, err := runner.Run(ctx, requestFor(strategy))
			require.ErrorIs(t, err, context.Canceled)
			assert.Equal(t, strategy != testframework.SSH, cleaned, "never clean a caller-owned SSH target")
			assert.Equal(t, testframework.Cancelled, result.Status)
			assert.NotContains(t, result.Error, "collect TMT run state")

			exists, err := fileutils.Exists(ctx.FS(), filepath.Dir(runID))
			require.NoError(t, err)
			assert.False(t, exists)

			_, err = fileutils.ReadFile(ctx.FS(), filepath.Join(workDir, "tmt", "tmt-output.log"))
			require.NoError(t, err, "cancellation must retain the native log")
			exists, err = fileutils.Exists(ctx.FS(), filepath.Join(workDir, "tmt", "ssh-sockets", "guest.socket"))
			require.NoError(t, err)
			assert.False(t, exists, "runtime control sockets must not be collected")
		})
	}
}
