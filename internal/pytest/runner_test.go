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

	"github.com/microsoft/azure-linux-dev-tools/internal/artifact"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/testctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/pytest"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/cachekey"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	pytestModule       = "pytest"
	installNone        = "none"
	missingEnvironment = "missing"
)

func request() testframework.RunRequest {
	return testframework.RunRequest{
		Execution:   testframework.ExecutionSpec{Strategy: testframework.Host},
		SubjectName: "curl", WorkDir: "/work/smoke", CacheDir: "/cache",
		Image: &artifact.Artifact{Path: "/images/base.raw", Format: "raw"}, InputsPath: "/work/inputs.json",
		JUnitPath: "/reports/smoke.xml", Packages: []artifact.Artifact{{Path: "/rpms/curl.rpm", Format: "rpm"}},
		Capabilities: []string{"bootable", "ssh"}, Properties: map[string]string{"arch": "aarch64"},
	}
}

func write(t *testing.T, ctx *testctx.TestCtx, path, content string) {
	t.Helper()
	require.NoError(t, fileutils.MkdirAll(ctx.FS(), filepath.Dir(path)))
	require.NoError(t, fileutils.WriteFile(ctx.FS(), path, []byte(content), fileperms.PrivateFile))
}

func preparedNone(t *testing.T, ctx *testctx.TestCtx, identity string) {
	t.Helper()
	require.NoError(t, ctx.OSEnv().Chdir("/tests"))
	require.NoError(t, fileutils.MkdirAll(ctx.FS(), "/tests"))

	dir := filepath.Join("/cache", "python", cachekey.From("pytest", "smoke", identity, installNone))
	write(t, ctx, filepath.Join(dir, "bin/python3"), "caller Python")
	write(t, ctx, filepath.Join(dir, "pyvenv.cfg"), "caller configuration")
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
	privateSource := commands[2].Dir
	assert.True(t, strings.HasPrefix(filepath.Base(filepath.Dir(privateSource)), "azldev-pytest-"))
	assert.NotEqual(t, "/tests", privateSource)
	assert.Equal(t,
		[]string{"-m", "pip", "install", "--quiet", "-r", privateSource + "/requirements.txt"}, commands[1].Args[1:])
	assert.Equal(t, []string{
		"-m", "pytest", "cases/test_one.py", "cases/nested/test_two.py", "unmatched*.py", "cases",
		"--image=/images/base.raw", "curl", "curl", "/work/inputs.json", "bootable,ssh",
		`{"arch":"aarch64"}`, "literal*.py", "-o", "cache_dir=" + privateSource + "/.pytest_cache",
		"--junit-xml", "/reports/smoke.xml", "--log-cli-level=DEBUG",
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
			preparedNone(t, ctx, "")

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
	for _, mode := range []string{"pyproject", "requirements", installNone} {
		t.Run(mode, func(t *testing.T) {
			ctx := testctx.NewCtx()
			ctx.CmdFactory.RegisterCommandInSearchPath("python3")
			require.NoError(t, fileutils.MkdirAll(ctx.FS(), "/tests"))

			if mode == installNone {
				preparedNone(t, ctx, "/tests")
			}

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
			if mode != installNone {
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
	assert.NotEqual(t, "/checkout/suite", commands[2].Dir)
	assert.True(t, strings.HasSuffix(commands[2].Dir, "/source/suite"))
	assert.Equal(t, []string{"-m", "pip", "install", "--quiet", "-e", commands[2].Dir}, commands[1].Args[1:])
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
	preparedNone(t, ctx, "")
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
	preparedNone(t, ctx, "")

	runner, err := pytest.New("smoke", map[string]any{
		"extra-args": []string{"image:{image-path}", "properties:{properties}", "inputs:{inputs-path}"},
	})
	require.NoError(t, err)

	req := request()
	req.Image = nil
	req.Properties = nil
	req.JUnitPath = ""
	ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
		if len(command.Args) > 2 && command.Args[2] == pytestModule {
			assert.Equal(t, []string{
				"-m", "pytest", "image:", "properties:{}", "inputs:/work/inputs.json",
				"-o", "cache_dir=" + command.Dir + "/.pytest_cache",
			},
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

func TestNoneRequiresCallerPreparedRuntimeWithoutInstalling(t *testing.T) {
	for _, stage := range []string{missingEnvironment, "partial", "missing-pytest", "prepared"} {
		t.Run(stage, func(t *testing.T) {
			ctx := testctx.NewCtx()
			require.NoError(t, ctx.OSEnv().Chdir("/tests"))
			write(t, ctx, "/tests/test_original.py", "original")

			dir := "/cache/python/2091d6e46f973f9f0b773facba0d69e19da73016c41c1f62051a74c04788ffcc"

			if stage != missingEnvironment {
				preparedNone(t, ctx, "")
			}

			if stage == "partial" {
				require.NoError(t, ctx.FS().Remove(filepath.Join(dir, "pyvenv.cfg")))
			}

			var commands []*exec.Cmd

			ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
				commands = append(commands, command)
				assert.Equal(t, filepath.Join(dir, "bin/python3"), command.Args[0])
				assert.NotContains(t, command.Args, "pip")
				assert.NotContains(t, command.Args, "venv")

				if stage == "missing-pytest" {
					return errors.New("No module named pytest")
				}

				if len(command.Args) > 2 && command.Args[2] == pytestModule {
					write(t, ctx, command.Dir+"/test_original.py", "private modification")
					write(t, ctx, request().JUnitPath, "<testsuites/>")
				}

				return nil
			}
			runner, err := pytest.New("smoke", nil)
			require.NoError(t, err)

			result, err := runner.Run(ctx, request())
			if stage == "prepared" {
				require.NoError(t, err)
				assert.Equal(t, testframework.Passed, result.Status)
				require.Len(t, commands, 2)
				assert.Equal(t, "-B", commands[0].Args[1])
				assert.Contains(t, commands[0].Args[3], "import pytest")
			} else {
				require.ErrorContains(t, err, "'install=none' requires a caller-prepared environment")
				assert.Equal(t, testframework.Errored, result.Status)
			}

			data, readErr := fileutils.ReadFile(ctx.FS(), "/tests/test_original.py")
			require.NoError(t, readErr)
			assert.Equal(t, "original", string(data))

			if stage != missingEnvironment {
				data, readErr = fileutils.ReadFile(ctx.FS(), filepath.Join(dir, "bin/python3"))
				require.NoError(t, readErr)
				assert.Equal(t, "caller Python", string(data))
			}

			if stage == missingEnvironment {
				exists, checkErr := fileutils.Exists(ctx.FS(), "/cache")
				require.NoError(t, checkErr)
				assert.False(t, exists, "missing caller-prepared cache must not be created")
			} else {
				entries, readErr := fileutils.ReadDir(ctx.FS(), "/cache")
				require.NoError(t, readErr)
				require.Len(t, entries, 1)
				assert.Equal(t, "python", entries[0].Name())
			}
		})
	}
}

func TestOverlappingRequestsNeverMutateCallerSourcesOrEachOther(t *testing.T) {
	ctx := testctx.NewCtx()
	ctx.CmdFactory.RegisterCommandInSearchPath("python3")
	write(t, ctx, "/tests/pyproject.toml", "original")
	write(t, ctx, "/tests/test_original.py", "original")

	runner, err := pytest.New("smoke", map[string]any{
		"working-dir": "/tests", "install": "pyproject",
		"test-paths": []string{"/tests/test_original.py"},
	})
	require.NoError(t, err)

	var sources, pythons []string

	insideFirst := false
	ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
		if len(command.Args) > 2 && command.Args[2] == "pip" {
			sources = append(sources, command.Dir)
			pythons = append(pythons, command.Args[0])
			data, readErr := fileutils.ReadFile(ctx.FS(), command.Dir+"/pyproject.toml")
			require.NoError(t, readErr)
			assert.Equal(t, "original", string(data))
			write(t, ctx, command.Dir+"/pyproject.toml", "editable install metadata")
		}

		if len(command.Args) <= 2 || command.Args[2] != pytestModule {
			return nil
		}

		write(t, ctx, command.Dir+"/test_original.py", "pytest mutation")
		assert.Equal(t, "test_original.py", command.Args[3], "absolute selectors must target the private copy")
		assert.Contains(t, command.Args, "cache_dir="+command.Dir+"/.pytest_cache")
		assert.Contains(t, command.Env, "PYTHONDONTWRITEBYTECODE=1")

		if !insideFirst {
			insideFirst = true
			second := request()
			second.WorkDir = "/work/second"
			second.JUnitPath = "/reports/second.xml"
			secondResult, runErr := runner.Run(ctx, second)
			require.NoError(t, runErr)
			assert.Equal(t, testframework.Passed, secondResult.Status)

			exists, checkErr := fileutils.Exists(ctx.FS(), command.Dir+"/pyproject.toml")
			require.NoError(t, checkErr)
			assert.True(t, exists, "second run cleanup must not remove the first run's source")
		}

		write(t, ctx, command.Args[len(command.Args)-1], "<testsuites/>")

		return nil
	}
	_, err = runner.Run(ctx, request())
	require.NoError(t, err)
	require.Len(t, sources, 2)
	require.Len(t, pythons, 2)
	assert.NotEqual(t, sources[0], sources[1])
	assert.NotEqual(t, pythons[0], pythons[1])

	for _, source := range sources {
		exists, checkErr := fileutils.Exists(ctx.FS(), filepath.Dir(source))
		require.NoError(t, checkErr)
		assert.False(t, exists, "private source and environment must both be cleaned")
	}

	for _, filename := range []string{"pyproject.toml", "test_original.py"} {
		data, readErr := fileutils.ReadFile(ctx.FS(), "/tests/"+filename)
		require.NoError(t, readErr)
		assert.Equal(t, "original", string(data))
	}
}

func TestProjectRootWithNestedCacheKeepsCallerContentsUnchanged(t *testing.T) {
	for _, mode := range []string{"pyproject", installNone} {
		for _, sourceOverride := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/source-override=%t", mode, sourceOverride), func(t *testing.T) {
				ctx := testctx.NewCtx()
				ctx.CmdFactory.RegisterCommandInSearchPath("python3")

				const root = "/fixture"

				req := request()
				req.CacheDir = root + "/build/work/test-cache"
				req.WorkDir = root + "/build/work/output"
				req.JUnitPath = ""
				require.NoError(t, fileutils.MkdirAll(ctx.FS(), req.WorkDir))

				raw := map[string]any{"working-dir": root, "install": mode}
				if sourceOverride {
					raw["working-dir"] = "."
					raw["source"] = map[string]any{
						"git-url": "https://example.test/fixture", "ref": strings.Repeat("a", 40),
					}
					req.Execution.SourceDir = root
				}

				originals := map[string]string{
					root + "/pyproject.toml":   "original project",
					root + "/test_example.py":  "original test",
					req.CacheDir + "/keep.txt": "caller cache",
				}

				preparedDir := filepath.Join(req.CacheDir, "python", cachekey.From("pytest", "smoke", root, installNone))
				if mode == installNone {
					originals[preparedDir+"/bin/python3"] = "prepared Python"
					originals[preparedDir+"/pyvenv.cfg"] = "prepared configuration"
				}

				for path, data := range originals {
					write(t, ctx, path, data)
				}

				before, err := fileutils.Glob(ctx.FS(), root+"/**/*")
				require.NoError(t, err)

				var runSource string

				ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
					if mode == installNone {
						assert.Equal(t, preparedDir+"/bin/python3", command.Args[0])
						assert.NotContains(t, command.Args, "pip")
						assert.NotContains(t, command.Args, "venv")
					}

					if len(command.Args) > 2 && command.Args[2] == "pip" {
						write(t, ctx, command.Dir+"/pyproject.toml", "editable install mutation")
					}

					if len(command.Args) > 2 && command.Args[2] == pytestModule {
						runSource = command.Dir
						assert.False(t, strings.HasPrefix(runSource, root+"/"))
						write(t, ctx, runSource+"/test_example.py", "pytest mutation")
						write(t, ctx, runSource+"/build/work/test-cache/keep.txt", "private cache mutation")
					}

					return nil
				}
				runner, err := pytest.New("smoke", raw)
				require.NoError(t, err)
				result, err := runner.Run(ctx, req)
				require.NoError(t, err)
				assert.Equal(t, testframework.Passed, result.Status)
				require.NotEmpty(t, runSource)
				exists, err := fileutils.Exists(ctx.FS(), filepath.Dir(runSource))
				require.NoError(t, err)
				assert.False(t, exists, "the private runtime must be removed")

				after, err := fileutils.Glob(ctx.FS(), root+"/**/*")
				require.NoError(t, err)
				assert.Equal(t, before, after, "no runtime files may be added to the caller tree")

				for path, original := range originals {
					data, readErr := fileutils.ReadFile(ctx.FS(), path)
					require.NoError(t, readErr)
					assert.Equal(t, original, string(data), path)
				}
			})
		}
	}
}

func TestSetupFailureAndCancellationCannotPoisonNextRun(t *testing.T) {
	for _, stage := range []string{"venv", "pip"} {
		for _, cancelled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancelled=%t", stage, cancelled), func(t *testing.T) {
				ctx := testctx.NewCtx()

				cancelCtx, cancel := context.WithCancel(t.Context())
				defer cancel()

				ctx.Ctx = cancelCtx
				ctx.CmdFactory.RegisterCommandInSearchPath("python3")
				write(t, ctx, "/tests/requirements.txt", "pytest")

				runner, err := pytest.New("smoke", map[string]any{"working-dir": "/tests", "install": "requirements"})
				require.NoError(t, err)

				var environments []string

				ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
					if len(command.Args) > 2 && command.Args[2] == "venv" {
						environments = append(environments, command.Args[3])
						write(t, ctx, command.Args[3]+"/bin/python3", "partial Python")
					}

					if len(command.Args) > 2 && command.Args[2] == stage {
						write(t, ctx, environments[0]+"/partial", "partial setup")

						if cancelled {
							cancel()

							return nil
						}

						return errors.New("setup failed")
					}

					return nil
				}
				result, err := runner.Run(ctx, request())
				require.Error(t, err)

				if cancelled {
					require.ErrorIs(t, err, context.Canceled)
					assert.Equal(t, testframework.Cancelled, result.Status)
				}

				require.Len(t, environments, 1)
				exists, checkErr := fileutils.Exists(ctx.FS(), filepath.Dir(environments[0]))
				require.NoError(t, checkErr)
				assert.False(t, exists)

				ctx.Ctx = t.Context()
				ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
					if len(command.Args) > 2 && command.Args[2] == "venv" {
						assert.NotEqual(t, environments[0], command.Args[3])
					}

					if len(command.Args) > 2 && command.Args[2] == pytestModule {
						write(t, ctx, request().JUnitPath, "<testsuites/>")
					}

					return nil
				}
				result, err = runner.Run(ctx, request())
				require.NoError(t, err)
				assert.Equal(t, testframework.Passed, result.Status)
			})
		}
	}
}
