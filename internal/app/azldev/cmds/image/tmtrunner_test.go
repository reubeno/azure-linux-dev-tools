// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//nolint:testpackage // Need access to package-private helpers (mergedPipExtras, buildTmtArgv, ...).
package image

import (
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergedPipExtras(t *testing.T) {
	t.Run("auto-derived from how=virtual", func(t *testing.T) {
		got := mergedPipExtras(&projectconfig.TmtConfig{
			Provision: projectconfig.TmtProvisionConfig{How: projectconfig.TmtProvisionHowVirtual},
		})
		assert.Equal(t, []string{"provision-virtual"}, got)
	})

	t.Run("user extras unioned and deduped", func(t *testing.T) {
		got := mergedPipExtras(&projectconfig.TmtConfig{
			Provision: projectconfig.TmtProvisionConfig{How: projectconfig.TmtProvisionHowVirtual},
			PipExtras: []string{"provision-virtual", "report-junit"},
		})
		// sorted, deduped
		assert.Equal(t, []string{"provision-virtual", "report-junit"}, got)
	})

	t.Run("no how, only user extras", func(t *testing.T) {
		got := mergedPipExtras(&projectconfig.TmtConfig{
			PipExtras: []string{"foo", "bar"},
		})
		assert.Equal(t, []string{"bar", "foo"}, got)
	})

	t.Run("empty", func(t *testing.T) {
		got := mergedPipExtras(&projectconfig.TmtConfig{})
		assert.Empty(t, got)
	})
}

func TestBuildTmtArgv(t *testing.T) {
	t.Run("minimal", func(t *testing.T) {
		got := buildTmtArgv(nil, &projectconfig.TmtConfig{
			Plan: "/plans/smoke",
		}, "/run/dir", "/img.qcow2", "")
		assert.Equal(t, []string{
			"run", "-a", "--workdir-root", "/run", "-i", "/run/dir",
			"plan", "-n", "/plans/smoke",
			"provision", "--image", "/img.qcow2",
		}, got)
	})

	t.Run("with how, context, and step extras", func(t *testing.T) {
		got := buildTmtArgv(nil, &projectconfig.TmtConfig{
			Plan: "/plans/smoke",
			Provision: projectconfig.TmtProvisionConfig{
				How: projectconfig.TmtProvisionHowVirtual,
			},
			Context: map[string][]string{
				"distro": {"fedora-rawhide"},
				"arch":   {"x86_64", "aarch64"},
			},
			RunExtraArgs:       []string{"-vvv"},
			PlanExtraArgs:      []string{"--filter", "tag:smoke"},
			ProvisionExtraArgs: []string{"--memory", "4096"},
		}, "/run/dir", "/img.qcow2", "/run/dir/junit.xml")
		assert.Equal(t, []string{
			// context is sorted alphabetically by key, multi-valued entries preserve order
			"-c", "arch=x86_64",
			"-c", "arch=aarch64",
			"-c", "distro=fedora-rawhide",
			"-vvv",
			"run", "-a", "--workdir-root", "/run", "-i", "/run/dir",
			"plan", "-n", "/plans/smoke", "--filter", "tag:smoke",
			"provision", "-h", "virtual", "--image", "/img.qcow2", "--memory", "4096",
			"report", "-h", "junit", "--file", "/run/dir/junit.xml",
		}, got)
	})

	t.Run("junit only when junitOutPath set", func(t *testing.T) {
		got := buildTmtArgv(nil, &projectconfig.TmtConfig{Plan: "/p"}, "/r", "/i.qcow2", "")
		for _, a := range got {
			assert.NotEqual(t, "report", a)
		}
	})
}

func TestParseTmtResultsYAML_EmptySequence(t *testing.T) {
	got, err := parseTmtResultsYAML([]byte(`[]`))
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestParseTmtResultsYAML_Sequence(t *testing.T) {
	// A simplified results.yaml (top-level sequence).
	yamlInput := []byte(`- name: /test/foo
  result: pass
  duration: 00:00:03
  log:
    - /tmt/runs/r1/plans/p/execute/data/foo/output.txt
- name: /test/bar
  result: fail
  duration: 00:01:30
`)
	got, err := parseTmtResultsYAML(yamlInput)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "/test/foo", got[0].name)
	assert.Equal(t, "pass", got[0].status)
	assert.InDelta(t, 3.0, got[0].durationSeconds, 0.001)
	assert.Equal(t, "/tmt/runs/r1/plans/p/execute/data/foo/output.txt", got[0].outputPath)
	assert.Equal(t, "/test/bar", got[1].name)
	assert.InDelta(t, 90.0, got[1].durationSeconds, 0.001)
}

func TestParseTmtResultsYAML_Mapping(t *testing.T) {
	yamlInput := []byte(`results:
  - name: /test/x
    result: pass
    duration: 01:02:03
`)
	got, err := parseTmtResultsYAML(yamlInput)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.InDelta(t, 3723.0, got[0].durationSeconds, 0.001)
}

func TestRenderCloudInitRuncmdsPlugin(t *testing.T) {
	got, err := renderCloudInitRuncmdsPlugin([]string{
		`firewall-cmd --add-port=10022/tcp || :`,
		`echo "hello \"world\""`,
	})
	require.NoError(t, err)
	// User strings are JSON-escaped, so quotes/backslashes round-trip safely.
	assert.Contains(t, got, `_AZLDEV_RUNCMDS = ["firewall-cmd --add-port=10022/tcp || :","echo \"hello \\\"world\\\"\""]`)
	assert.Contains(t, got, "import tmt.steps.provision.testcloud as _tc")
	assert.Contains(t, got, "_tc.TESTCLOUD_WORKAROUNDS.append(_cmd)")
}

func TestParseTmtDuration(t *testing.T) {
	cases := map[string]float64{
		"":         0,
		"03":       3,
		"00:30":    30,
		"01:02:03": 3723,
		"garbage":  0,
	}
	for input, want := range cases {
		got := parseTmtDuration(input)
		assert.InDelta(t, want, got, 0.001, "input=%q", input)
	}
}
