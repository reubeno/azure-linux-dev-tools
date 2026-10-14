// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pytest_test

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/testctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/pytest"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const pytestModule = "pytest"

func request() testframework.RunRequest {
	return testframework.RunRequest{
		Execution:   testframework.ExecutionSpec{Strategy: testframework.Host},
		SubjectName: "curl", WorkDir: "/work/smoke", CacheDir: "/cache",
		ImagePath: "/images/base.raw", InputsPath: "/work/inputs.json",
		JUnitPath: "/reports/smoke.xml", RPMPaths: []string{"/rpms/curl.rpm"},
		Capabilities: []string{"bootable", "ssh"}, Properties: map[string]string{"arch": "aarch64"},
	}
}

func write(t *testing.T, ctx *testctx.TestCtx, path, content string) {
	t.Helper()
	require.NoError(t, fileutils.MkdirAll(ctx.FS(), filepath.Dir(path)))
	require.NoError(t, fileutils.WriteFile(ctx.FS(), path, []byte(content), fileperms.PrivateFile))
}

func TestInvocationAndExpansion(t *testing.T) {
	ctx := testctx.NewCtx()
	ctx.CmdFactory.RegisterCommandInSearchPath("python3")
	ctx.VerboseValue = true
	write(t, ctx, "/tests/cases/test_one.py", "")
	write(t, ctx, "/tests/cases/nested/test_two.py", "")
	write(t, ctx, "/tests/requirements.txt", "pytest")

	raw := map[string]any{
		"working-dir": "/tests", "install": "requirements",
		"test-paths": []string{"cases/**/test_*.py", "unmatched*.py", "cases"},
		"extra-args": []string{
			"--image={image-path}", "{image-name}", "{subject-name}", "{inputs-path}",
			"{capabilities}", "{properties}", "literal*.py",
		},
		"external-metadata": map[string]any{"matrix": []int{1, 2}},
	}
	runner, err := pytest.New("smoke", raw)
	require.NoError(t, err)

	var commands []*exec.Cmd

	ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
		commands = append(commands, command)
		if len(command.Args) > 2 && command.Args[2] == pytestModule {
			write(t, ctx, request().JUnitPath, `<testsuites tests="1"/>`)
		}

		return nil
	}

	result, err := runner.Run(ctx, request())
	require.NoError(t, err)
	assert.Equal(t, testframework.Passed, result.Status)
	assert.Equal(t, "/reports/smoke.xml", result.JUnitPath)
	require.Len(t, commands, 3)
	assert.Equal(t, []string{"python3", "-m", "venv"}, commands[0].Args[:3])
	assert.Equal(t, []string{"-m", "pip", "install", "--quiet", "-r", "/tests/requirements.txt"}, commands[1].Args[1:])
	assert.Equal(t, "/tests", commands[2].Dir)
	assert.Equal(t, []string{
		"-m", "pytest", "cases/test_one.py", "cases/nested/test_two.py", "unmatched*.py", "cases",
		"--image=/images/base.raw", "curl", "curl", "/work/inputs.json", "bootable,ssh",
		`{"arch":"aarch64"}`, "literal*.py", "--junit-xml", "/reports/smoke.xml", "--log-cli-level=DEBUG",
	}, commands[2].Args[1:])
	assert.Contains(t, commands[2].Env, "AZLDEV_TEST_INPUTS=/work/inputs.json")
	assert.Contains(t, raw, "external-metadata")
}

type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit %d", e) }
func (e exitError) ExitCode() int { return int(e) }

func TestNativeOutcomes(t *testing.T) {
	for _, item := range []struct {
		name   string
		err    error
		status testframework.Status
		failed bool
	}{
		{"passed", nil, testframework.Passed, false},
		{"assertions", exitError(1), testframework.Failed, true},
		{"no-tests", exitError(5), testframework.Skipped, false},
		{"interrupt", exitError(2), testframework.Errored, false},
		{"internal-error", exitError(3), testframework.Errored, false},
		{"usage", exitError(4), testframework.Errored, false},
		{"launch", errors.New("launch failed"), testframework.Errored, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			ctx := testctx.NewCtx()
			ctx.CmdFactory.RegisterCommandInSearchPath("python3")

			runner, err := pytest.New("smoke", nil)
			require.NoError(t, err)

			ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
				if len(command.Args) > 2 && command.Args[2] == pytestModule {
					write(t, ctx, request().JUnitPath, `<testsuites/>`)

					if item.err != nil {
						_, _ = command.Stderr.Write([]byte("diagnostic"))
					}

					return item.err
				}

				return nil
			}
			result, err := runner.Run(ctx, request())
			assert.Equal(t, item.status, result.Status)
			assert.Equal(t, item.failed, errors.Is(err, testframework.ErrTestFailed))

			if item.status == testframework.Errored || item.failed {
				require.ErrorContains(t, err, "diagnostic")
				assert.Equal(t, err.Error(), result.Error)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestConfigurationAndRequirements(t *testing.T) {
	for name, raw := range map[string]map[string]any{
		"bad-dir":     {"working-dir": 1},
		"bad-paths":   {"test-paths": []any{1}},
		"bad-install": {"install": "unknown"},
		"bad-args":    {"extra-args": 1},
		"relative":    {"working-dir": "relative"},
		"bad-source":  {"source": map[string]any{"git-url": "url", "ref": "main"}},
		"escape": {
			"source":      map[string]any{"git-url": "url", "ref": strings.Repeat("a", 40)},
			"working-dir": "../escape",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := pytest.New("smoke", raw)
			require.Error(t, err)
		})
	}

	_, err := pytest.New("../escape", nil)
	require.Error(t, err)
	runner, err := pytest.New("smoke", nil)
	require.NoError(t, err)

	for _, strategy := range []testframework.Strategy{testframework.VM, testframework.Container, testframework.SSH} {
		_, err := runner.Requirements(testframework.ExecutionSpec{Strategy: strategy})
		require.Error(t, err)
	}

	requirements, err := runner.Requirements(testframework.ExecutionSpec{Strategy: testframework.Host})
	require.NoError(t, err)
	assert.True(t, requirements.PackagesSupported)
	assert.False(t, requirements.ImageRequired)
	assert.False(t, requirements.LiveImage)
	assert.Equal(t, []string{"qcow2", "raw", "vhd", "vhdx", "oci"}, requirements.ImageFormats)
}

func TestDryRunAndMissingManifest(t *testing.T) {
	ctx := testctx.NewCtx()
	ctx.DryRunValue = true
	ctx.CmdFactory.RunHandler = func(_ *exec.Cmd) error {
		t.Fatal("dry-run executed a command")

		return nil
	}
	runner, err := pytest.New("smoke", nil)
	require.NoError(t, err)
	result, err := runner.Run(ctx, request())
	require.NoError(t, err)
	assert.Equal(t, testframework.Planned, result.Status)

	entries, err := fileutils.ReadDir(ctx.FS(), "/")
	require.NoError(t, err)
	assert.Empty(t, entries)

	invalid := request()
	invalid.InputsPath = ""
	_, err = runner.Run(ctx, invalid)
	require.ErrorContains(t, err, "manifest")
}

func TestInstallModesAndSetupFailure(t *testing.T) {
	for _, mode := range []string{"pyproject", "requirements", "none"} {
		t.Run(mode, func(t *testing.T) {
			ctx := testctx.NewCtx()
			ctx.CmdFactory.RegisterCommandInSearchPath("python3")
			require.NoError(t, fileutils.MkdirAll(ctx.FS(), "/tests"))

			runner, err := pytest.New("smoke", map[string]any{"working-dir": "/tests", "install": mode})
			require.NoError(t, err)

			calls := 0
			ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
				calls++

				assert.NotContains(t, command.Args, "pytest")

				return errors.New("venv unavailable")
			}
			result, err := runner.Run(ctx, request())
			require.ErrorContains(t, err, "venv unavailable")
			assert.Equal(t, testframework.Errored, result.Status)
			assert.Equal(t, 1, calls)

			ctx.CmdFactory.RunHandler = nil
			if mode != "none" {
				_, err = runner.Run(ctx, request())
				require.ErrorContains(t, err, "requires")
			}
		})
	}
}

func TestPinnedSourceAndLocalOverride(t *testing.T) {
	ctx := testctx.NewCtx()
	ctx.CmdFactory.RegisterCommandInSearchPath("python3")
	write(t, ctx, "/checkout/suite/pyproject.toml", "")

	runner, err := pytest.New("smoke", map[string]any{
		"source":      map[string]any{"git-url": "https://example.test/suite", "ref": strings.Repeat("b", 40)},
		"working-dir": "suite", "install": "pyproject",
	})
	require.NoError(t, err)

	req := request()
	req.Execution.SourceDir = "/checkout"

	var commands []*exec.Cmd

	ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
		commands = append(commands, command)
		if len(command.Args) > 2 && command.Args[2] == pytestModule {
			write(t, ctx, req.JUnitPath, `<testsuites tests="1"/>`)
		}

		return nil
	}
	_, err = runner.Run(ctx, req)
	require.NoError(t, err)
	require.Len(t, commands, 3)
	assert.Equal(t, "/checkout/suite", commands[2].Dir)
	assert.Equal(t, []string{"-m", "pip", "install", "--quiet", "-e", "/checkout/suite"}, commands[1].Args[1:])
}

func TestCancellation(t *testing.T) {
	ctx := testctx.NewCtx()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	ctx.Ctx = cancelled
	ctx.CmdFactory.RegisterCommandInSearchPath("python3")
	ctx.CmdFactory.RunHandler = func(_ *exec.Cmd) error { return context.Canceled }
	runner, err := pytest.New("smoke", nil)
	require.NoError(t, err)
	result, err := runner.Run(ctx, request())
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, testframework.Cancelled, result.Status)
}

func TestMissingReportCannotReuseStaleResult(t *testing.T) {
	ctx := testctx.NewCtx()
	ctx.CmdFactory.RegisterCommandInSearchPath("python3")
	write(t, ctx, request().JUnitPath, `<testsuites tests="1"/>`)

	runner, err := pytest.New("smoke", nil)
	require.NoError(t, err)
	result, err := runner.Run(ctx, request())
	require.ErrorContains(t, err, "did not produce")
	assert.Equal(t, testframework.Errored, result.Status)
	assert.Empty(t, result.JUnitPath)
}

func TestRPMOnlyAndEmptyProperties(t *testing.T) {
	ctx := testctx.NewCtx()
	ctx.CmdFactory.RegisterCommandInSearchPath("python3")

	runner, err := pytest.New("smoke", map[string]any{
		"extra-args": []string{"image:{image-path}", "properties:{properties}", "inputs:{inputs-path}"},
	})
	require.NoError(t, err)

	req := request()
	req.ImagePath = ""
	req.Properties = nil
	req.JUnitPath = ""
	ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
		if len(command.Args) > 2 && command.Args[2] == pytestModule {
			assert.Equal(t, []string{"-m", "pytest", "image:", "properties:{}", "inputs:/work/inputs.json"},
				command.Args[1:])
			assert.Contains(t, command.Env, "AZLDEV_TEST_INPUTS=/work/inputs.json")
		}

		return nil
	}
	result, err := runner.Run(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, testframework.Passed, result.Status)
	assert.Empty(t, result.JUnitPath)
}
