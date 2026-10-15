// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lisa_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/artifact"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/testctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/lisa"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func request() testframework.RunRequest {
	return testframework.RunRequest{
		Execution:   testframework.ExecutionSpec{Strategy: testframework.VM, FrameworkDir: "/checkout"},
		SubjectName: "curl", WorkDir: "/work/smoke", CacheDir: "/cache",
		Image: &artifact.Artifact{Path: "/images/base.qcow2", Format: "qcow2"}, InputsPath: "/work/inputs.json",
		JUnitPath: "/reports/smoke.xml", Capabilities: []string{"bootable", "ssh"},
		Properties: map[string]string{"arch": "aarch64"},
	}
}

func write(t *testing.T, ctx *testctx.TestCtx, path, content string) {
	t.Helper()
	require.NoError(t, fileutils.MkdirAll(ctx.FS(), filepath.Dir(path)))
	require.NoError(t, fileutils.WriteFile(ctx.FS(), path, []byte(content), fileperms.PrivateFile))
}

func setup(t *testing.T) *testctx.TestCtx {
	t.Helper()

	ctx := testctx.NewCtx()
	ctx.CmdFactory.RegisterCommandInSearchPath("python3")
	ctx.CmdFactory.RegisterCommandInSearchPath("ssh-keygen")
	write(t, ctx, "/checkout/pyproject.toml", "source remains unchanged")
	write(t, ctx, "/checkout/lisa/microsoft/runbook/tiers/tier.yml", "extension: [../../testsuites]")

	return ctx
}

func assertClean(t *testing.T, ctx *testctx.TestCtx) {
	t.Helper()

	entries, err := fileutils.ReadDir(ctx.FS(), "/work/smoke")
	require.NoError(t, err)

	for _, entry := range entries {
		assert.NotContains(t, entry.Name(), "private-")
	}

	entries, err = fileutils.ReadDir(ctx.FS(), "/checkout")
	require.NoError(t, err)
	require.Len(t, entries, 2)

	data, err := fileutils.ReadFile(ctx.FS(), "/checkout/pyproject.toml")
	require.NoError(t, err)
	assert.Equal(t, "source remains unchanged", string(data))
}

func decoded[T any](t *testing.T, raw any) T {
	t.Helper()

	value, ok := raw.(T)
	require.True(t, ok)

	return value
}

func TestInvocationRunbookAndCandidates(t *testing.T) {
	ctx := setup(t)
	raw := map[string]any{
		"criteria": []any{
			map[string]any{"testcase-names": []any{"one", "two"}, "priority": []any{int64(0), int64(1)}, "tags": []any{"smoke"}},
			map[string]any{"area": "network", "category": "functional"},
		},
		"pip-pre-install": []string{"libvirt-python"}, "pip-extras": []string{"qemu"},
		"extra-args": []string{
			"--variable", "image:{image-path}", "name:{image-name}", "subject:{subject-name}",
			"inputs:{inputs-path}", "caps:{capabilities}", "properties:{properties}",
		},
		"external": map[string]any{"owners": []string{"team"}},
	}
	runner, err := lisa.New("smoke", raw)
	require.NoError(t, err)

	req := request()
	req.Packages = []artifact.Artifact{
		{Path: "/rpms/curl.rpm", Format: "rpm"}, {Path: "/other/libcurl.rpm", Format: "rpm"},
	}

	var (
		commands   []*exec.Cmd
		privateDir string
	)

	ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
		commands = append(commands, command)
		if filepath.Base(command.Args[0]) == "ssh-keygen" {
			privateDir = filepath.Dir(command.Args[6])
			write(t, ctx, command.Args[6], "private key")
			write(t, ctx, command.Args[6]+".pub", "public key")
		}

		if filepath.Base(command.Args[0]) != "lisa" {
			return nil
		}

		assert.Equal(t, req.WorkDir, command.Dir)
		assert.Equal(t, []string{
			"--variable", "image:/images/base.qcow2", "name:curl", "subject:curl",
			"inputs:/work/inputs.json", "caps:bootable,ssh", `properties:{"arch":"aarch64"}`,
		}, command.Args[7:])
		data, err := fileutils.ReadFile(ctx.FS(), command.Args[2])
		require.NoError(t, err)

		var runbook map[string]any
		require.NoError(t, yaml.Unmarshal(data, &runbook))
		assert.Equal(t, true, runbook["exit_with_failed_count"])
		testcases := decoded[[]any](t, runbook["testcase"])
		assert.Equal(t, map[string]any{
			"name": "one|two", "priority": []any{0, 1}, "tags": []any{"smoke"},
		}, decoded[map[string]any](t, testcases[0])["criteria"])
		assert.Equal(t, map[string]any{"area": "network", "category": "functional"},
			decoded[map[string]any](t, testcases[1])["criteria"])
		includes := decoded[[]any](t, runbook["include"])
		assert.Equal(t, filepath.Join(privateDir, "framework/lisa/microsoft/runbook/tiers/tier.yml"),
			decoded[map[string]any](t, includes[0])["path"])
		assert.Equal(t, []any{filepath.Join(privateDir, "framework/lisa/microsoft/testsuites")}, runbook["extension"])
		notifiers := decoded[[]any](t, runbook["notifier"])
		assert.Equal(t, map[string]any{"type": "junit", "path": req.JUnitPath}, notifiers[1])
		platform := decoded[map[string]any](t, decoded[[]any](t, runbook["platform"])[0])
		assert.Equal(t, "no", platform["keep_environment"])
		assert.Equal(t, filepath.Join(privateDir, "id_rsa"), platform["admin_private_key_file"])
		assert.Equal(t, map[string]any{"qemu": map[string]any{"qcow2": req.ImagePath()}}, platform["requirement"])

		transformers := decoded[[]any](t, runbook["transformer"])
		require.Len(t, transformers, 4)
		upload := decoded[map[string]any](t, transformers[0])
		assert.Equal(t, "file_uploader", upload["type"])
		assert.Equal(t, "environment_connected", upload["phase"])
		assert.Equal(t, "/rpms", upload["source"])
		assert.Equal(t, []any{"curl.rpm"}, upload["files"])
		install := decoded[map[string]any](t, transformers[3])
		assert.Equal(t, "script_file", install["type"])
		assert.Equal(t, []any{"azldev_upload_0", "azldev_upload_1", "azldev_upload_installer"}, install["depends_on"])
		assert.Equal(t, true, install["reboot"])

		script, err := fileutils.ReadFile(ctx.FS(), filepath.Join(privateDir, "install-candidates.sh"))
		require.NoError(t, err)
		assert.Contains(t, string(script), "dnf -y install ./*.rpm")
		assert.Contains(t, string(script), "dnf -y reinstall ./*.rpm")
		assert.Contains(t, string(script), "tdnf -y reinstall ./*.rpm")
		assert.NotContains(t, string(script), "--nodeps")
		assert.NotContains(t, string(script), "--force")
		write(t, ctx, req.JUnitPath, `<testsuites tests="1" failures="0" errors="0"/>`)

		return nil
	}
	result, err := runner.Run(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, testframework.Passed, result.Status)
	assert.Equal(t, req.JUnitPath, result.JUnitPath)
	require.Len(t, commands, 5)
	assert.Equal(t, []string{"-m", "pip", "install", "--quiet", "libvirt-python"}, commands[1].Args[1:])
	assert.Equal(t, []string{"-m", "pip", "install", "--quiet", "-e", filepath.Join(privateDir, "framework") + "[qemu]"},
		commands[2].Args[1:])
	assertClean(t, ctx)
	assert.Contains(t, raw, "external")
}

func TestCleanupOnEveryCommandFailure(t *testing.T) {
	for _, stage := range []string{"venv", "pip", "ssh-keygen", "lisa"} {
		t.Run(stage, func(t *testing.T) {
			ctx := setup(t)
			runner, err := lisa.New("smoke", map[string]any{"name": "check"})
			require.NoError(t, err)

			ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
				if filepath.Base(command.Args[0]) == stage || contains(command.Args, stage) {
					if stage == "ssh-keygen" {
						write(t, ctx, command.Args[6], "partial private key")
					}

					_, _ = command.Stderr.Write([]byte("native diagnostic"))

					return errors.New("injected failure")
				}

				return nil
			}
			result, err := runner.Run(ctx, request())
			require.ErrorContains(t, err, "injected failure")
			require.ErrorContains(t, err, "native diagnostic")
			assert.Equal(t, testframework.Errored, result.Status)
			assert.Equal(t, err.Error(), result.Error)
			assertClean(t, ctx)
		})
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}

	return false
}

func TestRequirementsAndInvalidRequests(t *testing.T) {
	runner, err := lisa.New("smoke", map[string]any{"testcase-name": "check"})
	require.NoError(t, err)

	req := request()
	requirements, err := runner.Requirements(req.Execution)
	require.NoError(t, err)
	assert.True(t, requirements.LiveImage)
	assert.True(t, requirements.ImageRequired)
	assert.True(t, requirements.PackagesSupported)
	assert.Equal(t, []string{"qcow2"}, requirements.ImageFormats)

	for _, strategy := range []testframework.Strategy{testframework.Host, testframework.Container, testframework.SSH} {
		_, err := runner.Requirements(testframework.ExecutionSpec{Strategy: strategy})
		require.Error(t, err)
	}

	_, err = runner.Requirements(testframework.ExecutionSpec{Strategy: testframework.VM})
	require.ErrorContains(t, err, "source")

	for name, modify := range map[string]func(*testframework.RunRequest){
		"raw":      func(req *testframework.RunRequest) { req.Image = &artifact.Artifact{Path: "/base.raw", Format: "raw"} },
		"relative": func(req *testframework.RunRequest) { req.WorkDir = "relative" },
		"rpm": func(req *testframework.RunRequest) {
			req.Packages = []artifact.Artifact{{Path: "candidate.rpm", Format: "rpm"}}
		},
		"xml": func(req *testframework.RunRequest) { req.JUnitPath = "relative.xml" },
	} {
		t.Run(name, func(t *testing.T) {
			ctx := testctx.NewCtx()
			ctx.DryRunValue = true
			req := request()
			modify(&req)
			result, err := runner.Run(ctx, req)
			require.Error(t, err)
			assert.Equal(t, testframework.Errored, result.Status)
		})
	}
}

func TestDryRunIsReadOnly(t *testing.T) {
	ctx := testctx.NewCtx()
	ctx.DryRunValue = true
	ctx.CmdFactory.RunHandler = func(_ *exec.Cmd) error {
		t.Fatal("dry-run executed a command")

		return nil
	}
	runner, err := lisa.New("smoke", map[string]any{"name": "check"})
	require.NoError(t, err)
	result, err := runner.Run(ctx, request())
	require.NoError(t, err)
	assert.Equal(t, testframework.Planned, result.Status)

	entries, err := fileutils.ReadDir(ctx.FS(), "/")
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestNativeReports(t *testing.T) {
	for _, item := range []struct {
		name   string
		xml    string
		status testframework.Status
	}{
		{"pass", `<testsuites tests="1" failures="0"/>`, testframework.Passed},
		{"fail", `<testsuites tests="1" failures="1"/>`, testframework.Failed},
		{"empty", `<testsuites tests="0"/>`, testframework.Skipped},
		{
			"skipped",
			`<testsuites tests="1"><testsuite><testcase><skipped/></testcase></testsuite></testsuites>`,
			testframework.Skipped,
		},
		{"infrastructure", `<testsuites tests="1" errors="1"/>`, testframework.Errored},
		{"malformed", `<not-xml`, testframework.Errored},
	} {
		t.Run(item.name, func(t *testing.T) {
			ctx := setup(t)
			runner, err := lisa.New("smoke", map[string]any{"name": "check"})
			require.NoError(t, err)

			ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
				if filepath.Base(command.Args[0]) == "lisa" {
					write(t, ctx, request().JUnitPath, item.xml)
				}

				return nil
			}
			result, err := runner.Run(ctx, request())
			assert.Equal(t, item.status, result.Status)

			switch item.status {
			case testframework.Failed:
				require.ErrorIs(t, err, testframework.ErrTestFailed)
			case testframework.Errored:
				require.Error(t, err)
			case testframework.Passed, testframework.Skipped, testframework.Cancelled, testframework.Planned:
				require.NoError(t, err)
			}

			assertClean(t, ctx)
		})
	}
}

func TestTypedConfiguration(t *testing.T) {
	for name, raw := range map[string]map[string]any{
		"missing":      {},
		"name":         {"name": 1},
		"names":        {"testcase-names": []any{1}},
		"criteria":     {"criteria": []any{"bad"}},
		"area":         {"criteria": map[string]any{"area": 1}},
		"tags":         {"criteria": map[string]any{"tags": []any{1}}},
		"priority":     {"criteria": map[string]any{"priority": "high"}},
		"args":         {"name": "check", "extra-args": []any{1}},
		"unpinned":     {"name": "check", "source": map[string]any{"git-url": "url", "ref": "main"}},
		"source-type":  {"name": "check", "source": "bad"},
		"empty-select": {"criteria": map[string]any{"external": "metadata"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := lisa.New("smoke", raw)
			require.Error(t, err)
		})
	}

	runner, err := lisa.New("smoke", map[string]any{
		"name": "check", "source": map[string]any{"git-url": "https://example.test/lisa", "ref": strings.Repeat("a", 40)},
	})
	require.NoError(t, err)
	_, err = runner.Requirements(testframework.ExecutionSpec{Strategy: testframework.VM})
	require.NoError(t, err)
	_, err = lisa.New("../escape", map[string]any{"name": "check"})
	require.Error(t, err)
}

func TestMissingReportCannotReuseStaleResult(t *testing.T) {
	ctx := setup(t)
	write(t, ctx, request().JUnitPath, `<testsuites tests="1"/>`)

	runner, err := lisa.New("smoke", map[string]any{"name": "check"})
	require.NoError(t, err)
	result, err := runner.Run(ctx, request())
	require.ErrorContains(t, err, "read LISA JUnit report")
	assert.Equal(t, testframework.Errored, result.Status)
	assert.Empty(t, result.JUnitPath)
	assertClean(t, ctx)
}

func TestMissingFrameworkAndNestedRunDirectory(t *testing.T) {
	runner, err := lisa.New("smoke", map[string]any{"name": "check"})
	require.NoError(t, err)

	for _, path := range []string{"/missing", "/checkout"} {
		t.Run(path, func(t *testing.T) {
			ctx := setup(t)
			req := request()
			req.Execution.FrameworkDir = path
			req.WorkDir = "/checkout/generated"
			result, err := runner.Run(ctx, req)
			require.Error(t, err)
			assert.Equal(t, testframework.Errored, result.Status)

			exists, err := fileutils.Exists(ctx.FS(), req.WorkDir)
			require.NoError(t, err)
			assert.False(t, exists)
		})
	}
}

func TestCancellationCleansPrivateRuntimeAndPreservesSource(t *testing.T) {
	for _, stage := range []string{"venv", "pip", "lisa"} {
		t.Run(stage, func(t *testing.T) {
			ctx := setup(t)

			cancelCtx, cancel := context.WithCancel(t.Context())
			defer cancel()

			ctx.Ctx = cancelCtx
			runner, err := lisa.New("smoke", map[string]any{"name": "check"})
			require.NoError(t, err)

			ctx.CmdFactory.RunHandler = func(command *exec.Cmd) error {
				if len(command.Args) > 2 && command.Args[2] == "pip" {
					write(t, ctx, command.Dir+"/pyproject.toml", "private build metadata")
				}

				if filepath.Base(command.Args[0]) == stage || contains(command.Args, stage) {
					cancel()

					return context.Canceled
				}

				return nil
			}
			result, err := runner.Run(ctx, request())
			require.ErrorIs(t, err, context.Canceled)
			assert.Equal(t, testframework.Cancelled, result.Status)
			assertClean(t, ctx)
		})
	}
}
