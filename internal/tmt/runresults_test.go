// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tmt_test

import (
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/tmt"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunCollectsNativeOutcomesAndJUnit(t *testing.T) {
	for _, test := range []struct {
		name, outcome string
		exit          error
		status        testframework.Status
	}{
		{"passed", "pass", nil, testframework.Passed},
		{"assertions", "fail", nativeExitError(1), testframework.Failed},
		{"infrastructure", "error", nativeExitError(2), testframework.Errored},
		{"all skipped", "skip", nativeExitError(4), testframework.Skipped},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := newRunContext(t)

			var cleanCalled bool

			ctx.CmdFactory.RunHandler = func(cmd *exec.Cmd) error {
				if filepath.Base(cmd.Args[0]) != tmtProgram {
					return nil
				}

				if slices.Contains(cmd.Args, "clean") {
					cleanCalled = true

					return nil
				}

				native := cmd.Args[slices.Index(cmd.Args, "--id")+1]
				writeResultData(t, ctx.FS(), filepath.Join(native, "plans/smoke/execute/results.yaml"),
					"- name: /case", "  result: "+test.outcome, "  duration: '00:00:03.5'",
					"  log: [data/output.txt]")
				writeFile(t, ctx, filepath.Join(native, "plans/smoke/execute/data/output.txt"), "test output")

				return test.exit
			}
			runner, err := tmt.New("test", nil)
			require.NoError(t, err)

			request := requestFor(testframework.Container)
			request.JUnitPath = "/work/result.xml"

			result, err := runner.Run(ctx, request)
			if test.status == testframework.Failed || test.status == testframework.Errored {
				require.ErrorIs(t, err, test.exit)
			} else {
				require.NoError(t, err)
				assert.False(t, cleanCalled, "successful and all-skipped runs must not invoke fallback cleanup")
			}

			require.NoError(t, result.Validate(err))
			assert.Equal(t, test.status, result.Status)
			assert.Equal(t, request.JUnitPath, result.JUnitPath)
			require.Len(t, result.Cases, 1)
			assert.Equal(t, "/case", result.Cases[0].Name)
			assert.Equal(t, test.outcome, result.Cases[0].Status)
			assert.Equal(t, 3500*time.Millisecond, result.Cases[0].Duration)
			output, readErr := fileutils.ReadFile(ctx.FS(), result.Cases[0].OutputPath)
			require.NoError(t, readErr)
			assert.Equal(t, "test output", string(output))

			report, readErr := fileutils.ReadFile(ctx.FS(), result.JUnitPath)
			require.NoError(t, readErr)
			assert.Contains(t, string(report), `name="/case"`)
			assert.Contains(t, string(report), `time="3.5"`)
		})
	}
}

func TestRunCannotCollectEarlierResults(t *testing.T) {
	ctx := newRunContext(t)
	old := filepath.Join(workDir, "tmt/plans/smoke/execute/results.yaml")
	writeResultData(t, ctx.FS(), old, "- name: /previous", "  result: pass")
	ctx.CmdFactory.RunHandler = func(*exec.Cmd) error {
		t.Fatal("reused result directory must fail before setup")

		return nil
	}
	runner, err := tmt.New("test", nil)
	require.NoError(t, err)

	request := requestFor(testframework.Host)
	request.JUnitPath = "/work/new.xml"
	result, err := runner.Run(ctx, request)
	require.ErrorContains(t, err, "fresh TMT result directory")
	assert.Equal(t, testframework.Errored, result.Status)
	assert.Empty(t, result.Cases)
	assert.Empty(t, result.JUnitPath)

	data, err := fileutils.ReadFile(ctx.FS(), old)
	require.NoError(t, err)
	assert.Contains(t, string(data), "/previous")
}

func TestRequestedReportCannotBeMissingOrMalformed(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		ctx := newRunContext(t)
		ctx.CmdFactory.RunHandler = func(cmd *exec.Cmd) error {
			if filepath.Base(cmd.Args[0]) == tmtProgram && malformed {
				native := cmd.Args[slices.Index(cmd.Args, "--id")+1]
				writeResultData(t, ctx.FS(), filepath.Join(native, "plans/smoke/execute/results.yaml"), "[")
			}

			return nil
		}
		runner, err := tmt.New("test", nil)
		require.NoError(t, err)

		request := requestFor(testframework.Host)
		request.JUnitPath = "/work/new.xml"
		result, err := runner.Run(ctx, request)
		require.Error(t, err)
		assert.Equal(t, testframework.Errored, result.Status)
		assert.Empty(t, result.JUnitPath)
	}
}

func TestCleanupFailureDominatesNativeTestFailure(t *testing.T) {
	ctx := newRunContext(t)
	cleanup := errors.New("cleanup failed")
	ctx.CmdFactory.RunHandler = func(cmd *exec.Cmd) error {
		if filepath.Base(cmd.Args[0]) != tmtProgram {
			return nil
		}

		if slices.Contains(cmd.Args, "clean") {
			return cleanup
		}

		return nativeExitError(1)
	}
	runner, err := tmt.New("test", nil)
	require.NoError(t, err)
	result, err := runner.Run(ctx, requestFor(testframework.Container))
	require.ErrorIs(t, err, cleanup)
	require.ErrorIs(t, err, testframework.ErrTestFailed)
	assert.Equal(t, testframework.Errored, result.Status)
}

func TestVMUsesTypedArtifactArchitecture(t *testing.T) {
	ctx := newRunContext(t)

	var args []string

	ctx.CmdFactory.RunHandler = func(cmd *exec.Cmd) error {
		if filepath.Base(cmd.Args[0]) == tmtProgram {
			args = cmd.Args
		}

		return nil
	}
	runner, err := tmt.New("test", nil)
	require.NoError(t, err)

	request := requestFor(testframework.VM)
	request.Image.Arch = "aarch64"
	_, err = runner.Run(ctx, request)
	require.NoError(t, err)

	index := slices.Index(args, "--arch")
	require.NotEqual(t, -1, index)
	assert.Equal(t, "aarch64", args[index+1])
}
