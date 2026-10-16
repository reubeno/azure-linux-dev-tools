// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tmt_test

import (
	"context"
	_ "embed"
	"errors"
	"io/fs"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/artifact"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/testctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/tmt"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/cachekey"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//go:embed requirements.txt
var pinnedRequirements []byte

const (
	sourceDir  = "/source"
	workDir    = "/work/run"
	planName   = "/plans/smoke"
	imageID    = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tmtProgram = "tmt"
	ociFormat  = "oci"
)

func TestNew(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"empty optional config", nil, ""},
		{"pinned source", sourceConfig(), ""},
		{"invalid source type", map[string]any{"source": "repo"}, "'tmt.source' must be a table"},
		{"missing git URL", map[string]any{"source": map[string]any{"ref": strings.Repeat("a", 40)}}, "'git-url'"},
		{"missing ref", map[string]any{"source": map[string]any{"git-url": "https://example.test/repo"}}, "'ref'"},
		{"noncommit ref", map[string]any{
			"source": map[string]any{"git-url": "https://example.test/repo", "ref": "main"},
		}, "'tmt.source'"},
		{"bad plan type", map[string]any{"plan": true}, "'tmt.plan' must be a string"},
		{"relative plan", map[string]any{"plan": "plans/smoke"}, "absolute plan name"},
		{"external metadata preserved", map[string]any{"z-extra": true, "a-extra": true}, ""},
		{"extra source metadata", map[string]any{"source": map[string]any{
			"git-url": "https://example.test/repo", "ref": strings.Repeat("a", 40), "external": true,
		}}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, err := tmt.New("example", test.raw)
			if test.want != "" {
				require.ErrorContains(t, err, test.want)
				assert.Nil(t, runner)

				return
			}

			require.NoError(t, err)
			assert.NotNil(t, runner)
		})
	}

	for _, name := range []string{"", ".", "..", "../outside", "nested/test", `nested\test`, "/outside"} {
		_, err := tmt.New(name, nil)
		require.ErrorContains(t, err, "invalid test name")
	}
}

func TestRequirements(t *testing.T) {
	runner, err := tmt.New("example", nil)
	require.NoError(t, err)

	for _, test := range []struct {
		strategy testframework.Strategy
		formats  []string
		live     bool
	}{
		{testframework.VM, []string{"qcow2", "raw", "vhd", "vhdx"}, true},
		{testframework.Container, []string{"oci"}, true},
		{testframework.SSH, nil, false},
		{testframework.Host, nil, false},
	} {
		t.Run(string(test.strategy), func(t *testing.T) {
			execution := executionFor(test.strategy)
			if test.strategy != testframework.VM {
				execution.Plan = ""
			}

			requirements, err := runner.Requirements(execution)
			require.NoError(t, err)
			assert.Equal(t, testframework.Requirements{
				ImageRequired: test.live, ImageFormats: test.formats,
				LiveImage: test.live, PackagesSupported: true,
			}, requirements)
		})
	}
}

func TestRequirementsRejectInvalidExecution(t *testing.T) {
	runner, err := tmt.New("example", nil)
	require.NoError(t, err)

	for _, test := range []struct {
		name   string
		change func(*testframework.ExecutionSpec)
		want   string
	}{
		{"unknown strategy", func(e *testframework.ExecutionSpec) { e.Strategy = "cloud" }, "unsupported"},
		{"missing source", func(e *testframework.ExecutionSpec) { e.SourceDir = "" }, "'tmt.source'"},
		{"relative source", func(e *testframework.ExecutionSpec) { e.SourceDir = "source" }, "absolute"},
		{"relative plan", func(e *testframework.ExecutionSpec) { e.Plan = "plan" }, "absolute plan"},
		{"missing VM plan", func(e *testframework.ExecutionSpec) { e.Plan = "" }, "required"},
		{"framework override", func(e *testframework.ExecutionSpec) { e.FrameworkDir = "/framework" }, "not supported"},
		{"SSH options on VM", func(e *testframework.ExecutionSpec) { e.SSH.Host = "host" }, "require execution strategy"},
		{"SSH missing host", func(e *testframework.ExecutionSpec) {
			*e = executionFor(testframework.SSH)
			e.SSH.Host = ""
		}, "requires a host and user"},
		{"SSH missing user", func(e *testframework.ExecutionSpec) {
			*e = executionFor(testframework.SSH)
			e.SSH.User = ""
		}, "requires a host and user"},
		{"SSH invalid host", func(e *testframework.ExecutionSpec) {
			*e = executionFor(testframework.SSH)
			e.SSH.Host = "-option"
		}, "invalid SSH host"},
		{"SSH invalid user", func(e *testframework.ExecutionSpec) {
			*e = executionFor(testframework.SSH)
			e.SSH.User = "two users"
		}, "invalid SSH user"},
		{"SSH negative port", func(e *testframework.ExecutionSpec) {
			*e = executionFor(testframework.SSH)
			e.SSH.Port = -1
		}, "SSH port"},
		{"SSH high port", func(e *testframework.ExecutionSpec) {
			*e = executionFor(testframework.SSH)
			e.SSH.Port = 65536
		}, "SSH port"},
		{"SSH relative key", func(e *testframework.ExecutionSpec) {
			*e = executionFor(testframework.SSH)
			e.SSH.KeyPath = "key"
		}, "key path must be absolute"},
	} {
		t.Run(test.name, func(t *testing.T) {
			execution := executionFor(testframework.VM)
			test.change(&execution)
			_, err := runner.Requirements(execution)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestRunAllStrategiesPreservesSourceAndNativeSteps(t *testing.T) {
	for _, strategy := range []testframework.Strategy{
		testframework.VM, testframework.Container, testframework.SSH, testframework.Host,
	} {
		t.Run(string(strategy), func(t *testing.T) {
			ctx := newRunContext(t)
			require.NoError(t, ctx.OSEnv().Chdir("/unrelated-cwd"))

			runner, err := tmt.New("example", map[string]any{"plan": "/catalog-plan"})
			require.NoError(t, err)

			request := requestFor(strategy)
			request.Packages = []artifact.Artifact{
				{Path: "/packages/main.rpm", Format: "rpm"}, {Path: "/packages/tests.rpm", Format: "rpm"},
			}
			request.JUnitPath = "/work/claimed-junit.xml"

			var (
				commands []*exec.Cmd
				exports  []*exec.Cmd
			)

			ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
				commands = append(commands, command)
				if filepath.Base(command.Args[0]) == tmtProgram {
					writeFile(t, ctx, command.Dir+"/.fmf/version", "private mutation\n")
					native := command.Args[slices.Index(command.Args, "--id")+1]
					writeResultData(t, ctx.FS(), filepath.Join(native, "plans/smoke/execute/results.yaml"),
						"- name: /case", "  result: pass")
				}

				return nil
			}
			ctx.CmdFactory.RunAndGetOutputHandler = func(command *exec.Cmd) (string, error) {
				if strings.Contains(command.Args[len(command.Args)-1], "importlib.metadata") {
					return string(pinnedRequirements), nil
				}

				if command.Args[0] == "qemu-img" {
					return `{"format":"qcow2"}`, nil
				}

				exports = append(exports, command)
				if command.Args[0] == "podman" {
					assert.Equal(t, []string{"podman", "pull", "--quiet", "oci-archive:/images/image"}, command.Args)

					return imageID + "\n", nil
				}

				assert.Equal(t, filepath.Join(filepath.Dir(commands[0].Args[3]), "source"), command.Dir)
				assert.Equal(t, []string{"plan", "export", "^" + planName + "$", "--how", "json"}, command.Args[1:])

				return `[{"name":"/plans/smoke","provision":[{"hardware":{` +
					`"memory":">= 4 GB","boot":{"method":"bios"},"cpu":{"cores":4}}}]}]`, nil
			}

			result, err := runner.Run(ctx, request)
			require.NoError(t, err)
			assert.Equal(t, testframework.Passed, result.Status)
			assert.Equal(t, "example", result.Name)
			assert.Equal(t, tmtProgram, result.Framework)
			assert.Equal(t, strategy, result.Strategy)
			assert.Equal(t, "/work/run/tmt", result.OutputDir)
			assert.Equal(t, request.JUnitPath, result.JUnitPath, "native cases generate the requested JUnit report")
			require.Len(t, commands, 3, "only venv, pip, and TMT run should execute")
			assert.Equal(t, []string{"-m", "venv"}, commands[0].Args[1:3])
			assert.Equal(t, filepath.Join(commands[0].Args[3], "bin/python3"), commands[1].Args[0])
			assert.Equal(t, []string{"-m", "pip", "install", "--quiet"}, commands[1].Args[1:5])

			command := commands[2]
			assert.Equal(t, filepath.Join(filepath.Dir(commands[0].Args[3]), "source"), command.Dir)
			assert.NotEqual(t, sourceDir, command.Dir)
			assert.Contains(t, command.Env, "TMT_BOOT_TIMEOUT=300")

			for _, untouched := range []string{"discover", "execute", "report", "finish", "reboot", "remove"} {
				assert.NotContains(t, command.Args, untouched)
			}

			nativeDir := command.Args[slices.Index(command.Args, "--id")+1]
			assert.True(t, strings.HasPrefix(nativeDir, "/var/tmp/azlt-"))
			expected := []string{
				"run", "--all", "--keep", "--workdir-root", filepath.Dir(nativeDir), "--id", nativeDir,
				"plan", "--name", "^" + planName + "$", "provision", "--how",
			}

			switch strategy {
			case testframework.VM:
				expected = append(expected, "virtual", "--become", "--image", "/images/image", "--user", "azureuser",
					"--hardware", "cpu.cores = 4", "--hardware", "memory >= 4 GB", "--hardware", "boot.method = uefi")

				assert.Equal(t, "tmt[provision-virtual]", commands[1].Args[len(commands[1].Args)-1])
				assert.Contains(t, command.Env, "TMT_PLUGINS=/work/run/tmt-plugins")
			case testframework.Container:
				expected = append(expected, "container", "--become", "--image", imageID,
					"--network-prefix", "azldev-"+cachekey.From(request.WorkDir)[:16])
			case testframework.SSH:
				expected = append(expected, "connect", "--become", "--guest", "machine.example",
					"--user", "tester", "--key", "/keys/id", "--port", "2222")
			case testframework.Host:
				expected = append(expected, "local", "--become")

				assert.Contains(t, command.Env, "TMT_FEELING_SAFE=1")
			}

			if strategy != testframework.VM {
				assert.Equal(t, "tmt", commands[1].Args[len(commands[1].Args)-1])

				if strategy == testframework.SSH {
					assert.Contains(t, command.Env, "TMT_PLUGINS=/work/run/tmt-plugins")
				} else {
					assert.NotContains(t, command.Env, "TMT_PLUGINS=/work/run/tmt-plugins")
				}
			}

			if strategy == testframework.SSH || strategy == testframework.Host {
				assert.Empty(t, exports, "only VM needs hardware export; SSH must not import or boot an image")
			} else {
				assert.Len(t, exports, 1)
			}

			expected = append(expected, "prepare", "--insert", "--how", "install", "--name", "azldev-candidate-rpms",
				"--package", "/packages/main.rpm", "--package", "/packages/tests.rpm")
			assert.Equal(t, expected, command.Args[1:])

			frozen, err := fileutils.ReadFile(ctx.FS(), filepath.Join(request.WorkDir, "python-environment.txt"))
			require.NoError(t, err)
			assert.Equal(t, strings.TrimSpace(string(pinnedRequirements)), strings.TrimSpace(string(frozen)))

			sourceEntries, err := fileutils.ReadDir(ctx.FS(), sourceDir)
			require.NoError(t, err)
			require.Len(t, sourceEntries, 1)
			assert.Equal(t, ".fmf", sourceEntries[0].Name())

			marker, err := fileutils.ReadFile(ctx.FS(), sourceDir+"/.fmf/version")
			require.NoError(t, err)
			assert.Equal(t, "1\n", string(marker))

			cwd, err := ctx.OSEnv().Getwd()
			require.NoError(t, err)
			assert.Equal(t, "/unrelated-cwd", cwd)
		})
	}
}

func TestImageTestsDoNotInsertPreparation(t *testing.T) {
	for _, strategy := range []testframework.Strategy{testframework.VM, testframework.Container} {
		t.Run(string(strategy), func(t *testing.T) {
			ctx := newRunContext(t)
			runner, err := tmt.New("example", nil)
			require.NoError(t, err)

			request := requestFor(strategy)

			var actual []string

			ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
				if filepath.Base(command.Args[0]) == tmtProgram {
					actual = command.Args
				}

				return successfulNativeRun(t, ctx, command)
			}

			_, err = runner.Run(ctx, request)
			require.NoError(t, err)
			require.NotEmpty(t, actual)
			assert.NotContains(t, actual, "prepare")
			assert.NotContains(t, actual, "--package")
		})
	}
}

func TestContainerUsesResolvedLocalImageID(t *testing.T) {
	ctx := newRunContext(t)
	runner, err := tmt.New("example", nil)
	require.NoError(t, err)

	request := requestFor(testframework.Container)
	request.Image = &artifact.Artifact{OCIImageID: imageID, Format: "oci"}
	ctx.CmdFactory.RunAndGetOutputHandler = func(command *exec.Cmd) (string, error) {
		if strings.Contains(command.Args[len(command.Args)-1], "importlib.metadata") {
			return string(pinnedRequirements), nil
		}

		t.Fatal("a resolved image ID must not be imported or pulled again")

		return "", nil
	}

	var actual []string

	ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
		if filepath.Base(command.Args[0]) == tmtProgram {
			actual = command.Args
		}

		return successfulNativeRun(t, ctx, command)
	}
	_, err = runner.Run(ctx, request)
	require.NoError(t, err)
	assert.Contains(t, actual, imageID)
}

func TestContainerRejectsInvalidImportedImageID(t *testing.T) {
	for _, output := range []string{"", "image:latest", imageID + "\n" + imageID, "sha256:xyz"} {
		ctx := newRunContext(t)
		ctx.CmdFactory.RunAndGetOutputHandler = func(*exec.Cmd) (string, error) { return output, nil }
		runner, err := tmt.New("example", nil)
		require.NoError(t, err)
		_, err = runner.Run(ctx, requestFor(testframework.Container))
		require.ErrorContains(t, err, "single imported image ID")
	}
}

func TestContainerAcceptsPodmanBareDigest(t *testing.T) {
	ctx := newRunContext(t)
	ctx.CmdFactory.RunAndGetOutputHandler = func(*exec.Cmd) (string, error) {
		return strings.TrimPrefix(imageID, "sha256:") + "\n", nil
	}
	runner, err := tmt.New("example", nil)
	require.NoError(t, err)
	_, err = runner.Run(ctx, requestFor(testframework.Container))
	require.NoError(t, err)
}

func TestHostOptionalPlanAndSSHDefaultAuthentication(t *testing.T) {
	for _, strategy := range []testframework.Strategy{testframework.Host, testframework.SSH} {
		ctx := newRunContext(t)
		runner, err := tmt.New("example", nil)
		require.NoError(t, err)

		request := requestFor(strategy)
		request.Execution.Plan = ""
		request.Execution.SSH.KeyPath = ""
		request.Execution.SSH.Port = 0

		var actual []string

		ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
			if filepath.Base(command.Args[0]) == tmtProgram {
				actual = command.Args
			}

			return successfulNativeRun(t, ctx, command)
		}

		_, err = runner.Run(ctx, request)
		require.NoError(t, err)
		require.NotEmpty(t, actual)
		assert.NotContains(t, actual, "--name")
		assert.NotContains(t, actual, "--key")
		assert.NotContains(t, actual, "--port")
		assert.NotContains(t, actual, "--image")
	}
}

func TestDryRunAndCancellationHaveNoSideEffects(t *testing.T) {
	for _, strategy := range []testframework.Strategy{
		testframework.VM, testframework.Container, testframework.SSH, testframework.Host,
	} {
		for _, cancelled := range []bool{false, true} {
			t.Run(string(strategy)+"/cancelled="+stringBool(cancelled), func(t *testing.T) {
				ctx := testctx.NewCtx()

				ctx.DryRunValue = !cancelled
				if cancelled {
					cancelCtx, cancel := context.WithCancel(t.Context())
					cancel()

					ctx.Ctx = cancelCtx
				}

				ctx.CmdFactory.RunHandler = func(*exec.Cmd) error {
					t.Fatal("must not execute a process")

					return nil
				}
				ctx.CmdFactory.RunAndGetOutputHandler = func(*exec.Cmd) (string, error) {
					t.Fatal("must not export plans or import images")

					return "", nil
				}
				runner, err := tmt.New("example", nil)
				require.NoError(t, err)

				result, err := runner.Run(ctx, requestFor(strategy))
				if cancelled {
					require.ErrorIs(t, err, context.Canceled)
					assert.Equal(t, testframework.Cancelled, result.Status)
				} else {
					require.NoError(t, err)
					assert.Equal(t, testframework.Planned, result.Status)
				}

				entries, err := fileutils.ReadDir(ctx.FS(), "/")
				require.NoError(t, err)
				assert.Empty(t, entries, "no writes, including cache and plugin creation")
			})
		}
	}
}

func TestRunPreflightFailsBeforeProcesses(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testframework.RunRequest)
		want   string
	}{
		{"source equals work", func(r *testframework.RunRequest) { r.WorkDir = sourceDir }, "must not overlap"},
		{"work inside source", func(r *testframework.RunRequest) { r.WorkDir = sourceDir + "/work" }, "must not overlap"},
		{"source inside work", func(r *testframework.RunRequest) { r.WorkDir = "/" }, "must not overlap"},
		{"relative work", func(r *testframework.RunRequest) { r.WorkDir = "work" }, "work directory must be absolute"},
		{"relative JUnit", func(r *testframework.RunRequest) { r.JUnitPath = "report.xml" }, "JUnit path must be absolute"},
		{"missing image", func(r *testframework.RunRequest) { r.Image = nil }, "image path"},
		{"unresolved container tag", func(r *testframework.RunRequest) {
			r.Execution.Strategy = testframework.Container
			r.Image.Format, r.Image.Path = ociFormat, "registry.example/image:latest"
		}, "image path"},
		{"unnormalized container identity", func(r *testframework.RunRequest) {
			r.Execution.Strategy = testframework.Container
			r.Image.Format, r.Image.Path = ociFormat, strings.TrimPrefix(imageID, "sha256:")
		}, "image path"},
		{"relative RPM", func(r *testframework.RunRequest) {
			r.Packages = []artifact.Artifact{{Path: "package.rpm", Format: "rpm"}}
		}, "RPM path"},
		{"host with image", func(r *testframework.RunRequest) {
			r.Execution.Strategy = testframework.Host
		}, "does not accept"},
		{"missing source tree", func(r *testframework.RunRequest) { r.Execution.SourceDir = "/missing" }, "does not exist"},
		{"missing fmf", func(r *testframework.RunRequest) { r.Execution.SourceDir = "/images" }, "fmf"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := newRunContext(t)
			ctx.CmdFactory.RunHandler = func(*exec.Cmd) error {
				t.Fatal("preflight must precede all commands")

				return nil
			}
			runner, err := tmt.New("example", nil)
			require.NoError(t, err)

			request := requestFor(testframework.VM)
			test.change(&request)
			result, err := runner.Run(ctx, request)
			require.ErrorContains(t, err, test.want)
			assert.Equal(t, testframework.Errored, result.Status)

			_, err = ctx.FS().Stat(workDir)
			assert.ErrorIs(t, err, fs.ErrNotExist)
		})
	}
}

func TestHostRequiresAzureLinux4AndSudo(t *testing.T) {
	for _, test := range []struct {
		release string
		sudo    bool
		want    string
	}{
		{"ID=fedora\nVERSION_ID=42\n", true, "requires Azure Linux 4"},
		{"ID=azurelinux\nVERSION_ID=3\n", true, "requires Azure Linux 4"},
		{"ID=azurelinux\nVERSION_ID=4\n", false, "sudo is required"},
	} {
		ctx := testctx.NewCtx()
		writeFile(t, ctx, "/etc/os-release", test.release)

		if test.sudo {
			ctx.CmdFactory.RegisterCommandInSearchPath("sudo")
		}

		ctx.CmdFactory.RunHandler = func(*exec.Cmd) error {
			t.Fatal("host prerequisite failure must precede commands")

			return nil
		}
		runner, err := tmt.New("example", nil)
		require.NoError(t, err)
		_, err = runner.Run(ctx, requestFor(testframework.Host))
		require.ErrorContains(t, err, test.want)
	}
}

func TestRunExitClassification(t *testing.T) {
	for _, test := range []struct {
		name     string
		err      error
		status   testframework.Status
		asserted bool
	}{
		{"assertions", nativeExitError(1), testframework.Failed, true},
		{"infrastructure", nativeExitError(2), testframework.Errored, false},
		{"no results", nativeExitError(3), testframework.Errored, false},
		{"launch error", errors.New("launch failed"), testframework.Errored, false},
		{"cancelled", context.Canceled, testframework.Cancelled, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := newRunContext(t)
			ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
				if filepath.Base(command.Args[0]) == tmtProgram {
					_, err := command.Stderr.Write([]byte("native diagnostic"))
					require.NoError(t, err)

					if test.asserted {
						native := command.Args[slices.Index(command.Args, "--id")+1]
						writeResultData(t, ctx.FS(), filepath.Join(native, "plans/smoke/execute/results.yaml"),
							"- name: /case", "  result: fail")
					}

					return test.err
				}

				return nil
			}
			runner, err := tmt.New("example", nil)
			require.NoError(t, err)
			result, err := runner.Run(ctx, requestFor(testframework.Host))
			require.ErrorIs(t, err, test.err)
			assert.Equal(t, test.status, result.Status)
			assert.Equal(t, test.asserted, errors.Is(err, testframework.ErrTestFailed))
			assert.Contains(t, result.Error, "native diagnostic")
			assert.Equal(t, "/work/run/tmt", result.OutputDir)
		})
	}
}

func TestSetupExitOneIsInfrastructure(t *testing.T) {
	ctx := newRunContext(t)
	ctx.CmdFactory.RunHandler = func(*exec.Cmd) error { return nativeExitError(1) }
	runner, err := tmt.New("example", nil)
	require.NoError(t, err)
	result, err := runner.Run(ctx, requestFor(testframework.Host))
	require.Error(t, err)
	require.NotErrorIs(t, err, testframework.ErrTestFailed)
	assert.Equal(t, testframework.Errored, result.Status)
}

func TestPinnedSourceUsesFreshCheckoutAndEnvironment(t *testing.T) {
	ctx := newRunContext(t)
	ctx.CmdFactory.RegisterCommandInSearchPath("git")

	runner, err := tmt.New("example", sourceConfig())
	require.NoError(t, err)

	request := requestFor(testframework.Host)
	request.Execution.SourceDir = ""

	var (
		commands []*exec.Cmd
		checkout string
	)

	ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
		commands = append(commands, command)
		if command.Args[0] == "git" && command.Args[1] == "clone" {
			checkout = command.Args[len(command.Args)-1]
			writeFile(t, ctx, checkout+"/.fmf/version", "1\n")
		}

		return successfulNativeRun(t, ctx, command)
	}

	_, err = runner.Run(ctx, request)
	require.NoError(t, err)
	require.Len(t, commands, 5)
	assert.Equal(t, "https://example.test/tests.git", commands[0].Args[3])
	assert.Contains(t, commands[0].Args, "--no-hardlinks")
	assert.True(t, strings.HasPrefix(filepath.Base(filepath.Dir(checkout)), "azldev-tmt-"))
	assert.Equal(t, []string{"git", "-C", checkout, "checkout", strings.Repeat("a", 40)}, commands[1].Args)
	assert.Equal(t, checkout, commands[4].Dir)
	firstCheckout := checkout
	firstPython := commands[3].Args[0]

	commands = nil
	request.WorkDir = "/work/next-run"
	_, err = runner.Run(ctx, request)
	require.NoError(t, err)
	require.Len(t, commands, 5, "each run needs a fresh pinned checkout and environment")
	assert.Equal(t, "clone", commands[0].Args[1])
	assert.Equal(t, checkout, commands[4].Dir)
	assert.NotEqual(t, firstCheckout, checkout)
	assert.NotEqual(t, firstPython, commands[3].Args[0])
	nativeDir := commands[4].Args[slices.Index(commands[4].Args, "--id")+1]
	assert.True(t, strings.HasPrefix(nativeDir, "/var/tmp/azlt-"))
	assert.NotContains(t, commands[4].Args, "/work/next-run/tmt")

	_, err = fileutils.ReadFile(ctx.FS(), "/work/next-run/tmt/tmt-output.log")
	require.NoError(t, err, "native output must be collected into the requested output directory")

	for _, path := range []string{filepath.Dir(firstCheckout), filepath.Dir(checkout)} {
		exists, checkErr := fileutils.Exists(ctx.FS(), path)
		require.NoError(t, checkErr)
		assert.False(t, exists, "source and environment must both be removed after use")
	}
}

func TestSourceContainingProjectCacheUsesIndependentRuntime(t *testing.T) {
	ctx := newRunContext(t)
	req := requestFor(testframework.Host)
	callerCache := sourceDir + "/build/work/test-cache"
	writeFile(t, ctx, callerCache+"/keep.txt", "caller cache")
	before, err := fileutils.Glob(ctx.FS(), sourceDir+"/**/*")
	require.NoError(t, err)

	var runSource, runtime string

	ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
		if len(command.Args) > 2 && command.Args[2] == "venv" {
			runtime = filepath.Dir(command.Args[3])
			assert.False(t, strings.HasPrefix(runtime, sourceDir+"/"))
		}

		if filepath.Base(command.Args[0]) == tmtProgram {
			runSource = command.Dir
			assert.Equal(t, filepath.Join(runtime, "source"), runSource)
			writeFile(t, ctx, runSource+"/.fmf/version", "private mutation")
			writeFile(t, ctx, runSource+"/build/work/test-cache/keep.txt", "private cache mutation")
		}

		return successfulNativeRun(t, ctx, command)
	}
	runner, err := tmt.New("example", nil)
	require.NoError(t, err)
	result, err := runner.Run(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, testframework.Passed, result.Status)
	require.NotEmpty(t, runSource)

	exists, err := fileutils.Exists(ctx.FS(), runtime)
	require.NoError(t, err)
	assert.False(t, exists, "the private source and environment must be removed")

	after, err := fileutils.Glob(ctx.FS(), sourceDir+"/**/*")
	require.NoError(t, err)
	assert.Equal(t, before, after, "no runtime files may be added to the caller tree")

	for path, original := range map[string]string{
		sourceDir + "/.fmf/version": "1\n",
		callerCache + "/keep.txt":   "caller cache",
	} {
		data, readErr := fileutils.ReadFile(ctx.FS(), path)
		require.NoError(t, readErr)
		assert.Equal(t, original, string(data), path)
	}
}

func TestRuntimeFailureAndCancellationCleanup(t *testing.T) {
	for _, stage := range []string{"venv", "pip", "tmt"} {
		t.Run(stage, func(t *testing.T) {
			ctx := newRunContext(t)

			cancelCtx, cancel := context.WithCancel(t.Context())
			defer cancel()

			ctx.Ctx = cancelCtx
			runner, err := tmt.New("example", nil)
			require.NoError(t, err)

			var privateDir string

			ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
				if len(command.Args) > 2 && command.Args[2] == "venv" {
					privateDir = filepath.Dir(command.Args[3])
					writeFile(t, ctx, command.Args[3]+"/bin/python3", "partial Python")
				}

				if filepath.Base(command.Args[0]) == stage || (len(command.Args) > 2 && command.Args[2] == stage) {
					cancel()

					return context.Canceled
				}

				return nil
			}
			result, err := runner.Run(ctx, requestFor(testframework.Host))
			require.ErrorIs(t, err, context.Canceled)
			assert.Equal(t, testframework.Cancelled, result.Status)
			require.NotEmpty(t, privateDir)
			exists, checkErr := fileutils.Exists(ctx.FS(), privateDir)
			require.NoError(t, checkErr)
			assert.False(t, exists)

			marker, readErr := fileutils.ReadFile(ctx.FS(), sourceDir+"/.fmf/version")
			require.NoError(t, readErr)
			assert.Equal(t, "1\n", string(marker))

			ctx.Ctx = t.Context()
			ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error { return successfulNativeRun(t, ctx, command) }
			retry := requestFor(testframework.Host)
			retry.WorkDir = "/work/retry"
			result, err = runner.Run(ctx, retry)
			require.NoError(t, err)
			assert.Equal(t, testframework.Passed, result.Status)
		})
	}
}

func TestInvalidFilesFailBeforeProcesses(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testctx.TestCtx, *testframework.RunRequest)
		want   string
	}{
		{"missing image", func(_ *testctx.TestCtx, request *testframework.RunRequest) {
			request.Image.Path = "/images/missing"
		}, "TMT input file"},
		{"directory image", func(_ *testctx.TestCtx, request *testframework.RunRequest) {
			request.Image.Path = "/images"
		}, "must be a regular file"},
		{"missing RPM", func(_ *testctx.TestCtx, request *testframework.RunRequest) {
			request.Packages = []artifact.Artifact{{Path: "/packages/missing.rpm", Format: "rpm"}}
		}, "TMT input file"},
		{"directory fmf marker", func(ctx *testctx.TestCtx, _ *testframework.RunRequest) {
			require.NoError(t, ctx.FS().Remove(sourceDir+"/.fmf/version"))
			require.NoError(t, fileutils.MkdirAll(ctx.FS(), sourceDir+"/.fmf/version"))
		}, "must be a regular file"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := newRunContext(t)
			ctx.CmdFactory.RunHandler = func(*exec.Cmd) error {
				t.Fatal("invalid inputs must fail before processes")

				return nil
			}
			request := requestFor(testframework.VM)
			test.change(ctx, &request)

			runner, err := tmt.New("example", nil)
			require.NoError(t, err)
			_, err = runner.Run(ctx, request)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestConfigSnapshotAndPlanOverride(t *testing.T) {
	raw := map[string]any{"plan": planName}
	runner, err := tmt.New("example", raw)
	require.NoError(t, err)

	raw["plan"] = "invalid"
	request := requestFor(testframework.VM)
	request.Execution.Plan = ""
	_, err = runner.Requirements(request.Execution)
	require.NoError(t, err)
	assert.Equal(t, "invalid", raw["plan"])
}

func sourceConfig() map[string]any {
	return map[string]any{
		"source": map[string]any{"git-url": "https://example.test/tests.git", "ref": strings.Repeat("a", 40)},
	}
}

func executionFor(strategy testframework.Strategy) testframework.ExecutionSpec {
	execution := testframework.ExecutionSpec{Strategy: strategy, SourceDir: sourceDir, Plan: planName}
	if strategy == testframework.SSH {
		execution.SSH.Host = "machine.example"
		execution.SSH.User = "tester"
		execution.SSH.KeyPath = "/keys/id"
		execution.SSH.Port = 2222
	}

	return execution
}

func requestFor(strategy testframework.Strategy) testframework.RunRequest {
	request := testframework.RunRequest{
		Execution: executionFor(strategy), WorkDir: workDir,
	}
	if strategy == testframework.VM || strategy == testframework.Container {
		request.Image = &artifact.Artifact{Path: "/images/image", Format: "qcow2"}
		if strategy == testframework.Container {
			request.Image.Format = ociFormat
		}
	}

	return request
}

func newRunContext(t *testing.T) *testctx.TestCtx {
	t.Helper()

	ctx := testctx.NewCtx()

	ctx.Ctx = t.Context()
	for _, program := range []string{"python3", "sudo", "podman", "qemu-img"} {
		ctx.CmdFactory.RegisterCommandInSearchPath(program)
	}

	writeFile(t, ctx, "/etc/os-release", "ID=azurelinux\nVERSION_ID=4.0\n")
	writeFile(t, ctx, sourceDir+"/.fmf/version", "1\n")
	writeFile(t, ctx, "/images/image", "image")
	writeFile(t, ctx, "/packages/main.rpm", "rpm")
	writeFile(t, ctx, "/packages/tests.rpm", "rpm")
	writeFile(t, ctx, "/keys/id", "fake key")
	ctx.CmdFactory.RunAndGetOutputHandler = func(command *exec.Cmd) (string, error) {
		if command.Args[0] == "qemu-img" {
			return `{"format":"qcow2"}`, nil
		}

		if command.Args[0] == "podman" {
			return imageID + "\n", nil
		}

		return `[{"name":"/plans/smoke","provision":[]}]`, nil
	}
	ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error { return successfulNativeRun(t, ctx, command) }

	return ctx
}

func successfulNativeRun(t *testing.T, ctx *testctx.TestCtx, command *exec.Cmd) error {
	t.Helper()

	if filepath.Base(command.Args[0]) == tmtProgram && !slices.Contains(command.Args, "clean") {
		native := command.Args[slices.Index(command.Args, "--id")+1]
		writeResultData(t, ctx.FS(), filepath.Join(native, "plans/smoke/execute/results.yaml"),
			"- name: /case", "  result: pass")
	}

	return nil
}

func writeFile(t *testing.T, ctx *testctx.TestCtx, path, content string) {
	t.Helper()
	require.NoError(t, fileutils.MkdirAll(ctx.FS(), filepath.Dir(path)))
	require.NoError(t, fileutils.WriteFile(ctx.FS(), path, []byte(content), fileperms.PrivateFile))
}

type nativeExitError int

func (e nativeExitError) Error() string { return "native failure" }
func (e nativeExitError) ExitCode() int { return int(e) }

func stringBool(value bool) string {
	if value {
		return "true"
	}

	return "false"
}
