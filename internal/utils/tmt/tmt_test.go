// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// These tests exercise unexported helpers (buildArgv, parseTmtDuration,
// parseResultsYAML, renderRuncmdsPlugin) so live inside the package.
//
//nolint:testpackage // intentionally white-box for unexported helpers
package tmt

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSourceValidate(t *testing.T) {
	const validSHA = "a6bcc6767229199f4f02b781d1d39df0835d894b"

	t.Run("valid", func(t *testing.T) {
		assert.NoError(t, Source{GitURL: "https://example.com/r.git", Ref: validSHA}.Validate("ctx"))
	})

	t.Run("missing url", func(t *testing.T) {
		assert.Error(t, Source{Ref: validSHA}.Validate("ctx"))
	})

	t.Run("missing ref", func(t *testing.T) {
		assert.Error(t, Source{GitURL: "https://example.com/r.git"}.Validate("ctx"))
	})

	t.Run("ref too short", func(t *testing.T) {
		err := Source{GitURL: "https://example.com/r.git", Ref: "abc1234"}.Validate("ctx")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidGitRef)
	})

	t.Run("ref non-hex", func(t *testing.T) {
		err := Source{
			GitURL: "https://example.com/r.git",
			Ref:    "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ",
		}.Validate("ctx")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidGitRef)
	})
}

func TestMergedPipExtras(t *testing.T) {
	t.Run("auto-derived from how=virtual", func(t *testing.T) {
		assert.Equal(t,
			[]string{"provision-virtual"},
			MergedPipExtras(ProvisionVirtual, nil))
	})

	t.Run("user extras unioned and deduped", func(t *testing.T) {
		assert.Equal(t,
			[]string{"provision-virtual", "report-junit"},
			MergedPipExtras(ProvisionVirtual, []string{"provision-virtual", "report-junit"}))
	})

	t.Run("empty how + user extras only", func(t *testing.T) {
		assert.Equal(t,
			[]string{"bar", "foo"},
			MergedPipExtras("", []string{"foo", "bar"}))
	})

	t.Run("empty everything", func(t *testing.T) {
		assert.Empty(t, MergedPipExtras("", nil))
	})
}

func TestPipExtraForProvisionHow(t *testing.T) {
	extra, ok := PipExtraForProvisionHow(ProvisionVirtual)
	assert.True(t, ok)
	assert.Equal(t, "provision-virtual", extra)

	_, ok = PipExtraForProvisionHow("nope")
	assert.False(t, ok)
}

func TestBuildArgv(t *testing.T) {
	t.Run("minimal", func(t *testing.T) {
		got := buildArgv(false, RunSpec{Plan: "/plans/smoke"}, "/work/tmt-suite-1234", "/img.qcow2")
		assert.Equal(t, []string{
			"run", "-a", "--workdir-root", "/work", "-i", "/work/tmt-suite-1234",
			"plan", "-n", "/plans/smoke",
			"provision", "--image", "/img.qcow2",
		}, got)
	})

	t.Run("with how, context, step extras, and JUnit", func(t *testing.T) {
		got := buildArgv(true, RunSpec{
			Plan: "/plans/smoke",
			Provision: ProvisionSpec{
				How:       ProvisionVirtual,
				ExtraArgs: []string{"--memory", "4096"},
			},
			Context: map[string][]string{
				"distro": {"fedora-rawhide"},
				"arch":   {"x86_64", "aarch64"},
			},
			RunExtraArgs:  []string{"--feeling-safe"},
			PlanExtraArgs: []string{"--filter", "tag:smoke"},
			JUnitOutPath:  "/run/dir/junit.xml",
		}, "/run/dir", "/img.qcow2")
		assert.Equal(t, []string{
			"-vv", // auto-injected because verbose=true and no -v in run-extra-args
			"-c", "arch=x86_64",
			"-c", "arch=aarch64",
			"-c", "distro=fedora-rawhide",
			"--feeling-safe",
			"run", "-a", "--workdir-root", "/run", "-i", "/run/dir",
			"plan", "-n", "/plans/smoke", "--filter", "tag:smoke",
			"provision", "-h", "virtual", "--image", "/img.qcow2", "--memory", "4096",
			"report", "-h", "junit", "--file", "/run/dir/junit.xml",
		}, got)
	})

	t.Run("user-supplied verbose flag suppresses auto-injection", func(t *testing.T) {
		got := buildArgv(true, RunSpec{
			Plan:         "/plans/x",
			RunExtraArgs: []string{"-vvv"},
		}, "/work/run", "/img.qcow2")
		// Should have only one -vvv (from user), no -vv prepended.
		assert.NotContains(t, got, "-vv")
		assert.Contains(t, got, "-vvv")
	})

	t.Run("junit only when JUnitOutPath set", func(t *testing.T) {
		got := buildArgv(false, RunSpec{Plan: "/p"}, "/run/dir", "/img.qcow2")
		for _, a := range got {
			assert.NotEqual(t, "report", a)
		}
	})
}

func TestParseTmtDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"":         0,
		"03":       3 * time.Second,
		"00:30":    30 * time.Second,
		"01:02:03": 1*time.Hour + 2*time.Minute + 3*time.Second,
		"garbage":  0,
	}
	for input, want := range cases {
		got := parseTmtDuration(input)
		assert.Equal(t, want, got, "input=%q", input)
	}
}

func TestParseResultsYAML_EmptySequence(t *testing.T) {
	got, err := parseResultsYAML([]byte(`[]`))
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestParseResultsYAML_Sequence(t *testing.T) {
	in := []byte(`- name: /test/foo
  result: pass
  duration: 00:00:03
  log:
    - /tmt/runs/r1/plans/p/execute/data/foo/output.txt
- name: /test/bar
  result: fail
  duration: 00:01:30
`)
	got, err := parseResultsYAML(yamlInput)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "/test/foo", got[0].Name)
	assert.Equal(t, "pass", got[0].Status)
	assert.Equal(t, 3*time.Second, got[0].Duration)
	assert.Equal(t, "/tmt/runs/r1/plans/p/execute/data/foo/output.txt", got[0].OutputPath)
	assert.Equal(t, 90*time.Second, got[1].Duration)
}

func TestParseResultsYAML_Mapping(t *testing.T) {
	in := []byte(`results:
  - name: /test/x
    result: pass
    duration: 01:02:03
`)
	got, err := parseResultsYAML(yamlInput)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, 1*time.Hour+2*time.Minute+3*time.Second, got[0].Duration)
}

func TestRenderRuncmdsPlugin(t *testing.T) {
	got, err := renderRuncmdsPlugin([]string{
		`firewall-cmd --add-port=10022/tcp || :`,
		`echo "hello \"world\""`,
	})
	require.NoError(t, err)
	// User strings are JSON-escaped, so quotes/backslashes round-trip safely.
	assert.Contains(t,
		got, `_AZLDEV_RUNCMDS = ["firewall-cmd --add-port=10022/tcp || :","echo \"hello \\\"world\\\"\""]`,
		"expected JSON-encoded array; got:\n%s", got)
	assert.Contains(t, got, "import tmt.steps.provision.testcloud as _tc")
	assert.Contains(t, got, "_tc.TESTCLOUD_WORKAROUNDS.append(_cmd)")
}
