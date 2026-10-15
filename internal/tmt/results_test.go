// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tmt_test

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/testctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/tmt"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeResultData(t *testing.T, fs opctx.FS, path string, lines ...string) {
	t.Helper()
	require.NoError(t, fileutils.MkdirAll(fs, filepath.Dir(path)))
	require.NoError(t, fileutils.WriteFile(fs, path, []byte(strings.Join(lines, "\n")+"\n"), fileperms.PrivateFile))
}

func TestCollectResultsAcrossPlans(t *testing.T) {
	fs := testctx.NewCtx().FS()
	writeResultData(t, fs, "/native/plans/a/execute/results.yaml",
		"- name: /tests/smoke",
		"  result: pass",
		"  original-result: pass",
		"  duration: '01:02:03'",
		"  log: [data/guest/default-0/tests/smoke-1/output.txt]",
		"  serial-number: 1",
		"  guest: {name: default-0, role: null}",
		"  context: {arch: [x86_64]}",
		"  subresult: [{name: /subtest, result: fail}]",
		"  check: []",
		"- name: /tests/slow",
		"  result: fail",
		"  duration: '00:03.5'",
		"  log: [data/slow/output.txt, data/slow/journal.txt]",
	)
	writeResultData(t, fs, "/native/plans/nested/b/execute/results.yaml",
		"results:",
		"  - name: /tests/optional",
		"    result: skip",
		"    duration: null",
		"    log: []",
		"external: metadata",
	)

	for _, wrong := range []string{
		"/native/results.yaml", "/native/plans/a/prepare/results.yaml",
		"/native/plans/a/finish/results.yaml", "/native/plans/a/execute/data/results.yaml",
		"/native/plans/a/prepare/nested/execute/results.yaml",
		"/native/plans/a/finish/nested/execute/results.yaml",
	} {
		writeResultData(t, fs, wrong, "this is not a results document")
	}

	entries, err := tmt.CollectResults(fs, "/native")
	require.NoError(t, err)
	assert.Equal(t, []tmt.ResultEntry{
		{
			Name: "/tests/smoke", Status: "pass", Duration: time.Hour + 2*time.Minute + 3*time.Second,
			OutputPath: "/native/plans/a/execute/data/guest/default-0/tests/smoke-1/output.txt",
		},
		{
			Name: "/tests/slow", Status: "fail", Duration: 3500 * time.Millisecond,
			OutputPath: "/native/plans/a/execute/data/slow/output.txt",
		},
		{Name: "/tests/optional", Status: "skip"},
	}, entries)
}

func TestCollectResultsAbsenceAndEmptySequence(t *testing.T) {
	fs := testctx.NewCtx().FS()
	entries, err := tmt.CollectResults(fs, "/missing")
	require.NoError(t, err)
	assert.Empty(t, entries)

	require.NoError(t, fileutils.MkdirAll(fs, "/native/plan/execute"))
	entries, err = tmt.CollectResults(fs, "/native")
	require.NoError(t, err)
	assert.Empty(t, entries)

	writeResultData(t, fs, "/native/plan/execute/results.yaml", "[]")
	entries, err = tmt.CollectResults(fs, "/native")
	require.NoError(t, err)
	assert.Empty(t, entries)

	_, err = tmt.CollectResults(fs, "relative")
	require.ErrorContains(t, err, "absolute")
}

func TestCollectResultsRejectMalformedData(t *testing.T) {
	for _, test := range []struct {
		name  string
		lines []string
		want  string
	}{
		{"invalid yaml", []string{"["}, "decode results"},
		{"empty document", nil, "decode results"},
		{"null document", []string{"null"}, "sequence"},
		{"scalar document", []string{"123"}, "sequence"},
		{"wrapper without results", []string{"metadata: ignored"}, "sequence"},
		{"null wrapper results", []string{"results: null"}, "sequence"},
		{"scalar wrapper results", []string{"results: 12"}, "sequence"},
		{"two documents", []string{"[]", "---", "[]"}, "exactly one"},
		{"invalid trailing document", []string{"[]", "---", "["}, "trailing"},
		{"nonmapping result", []string{"- pass"}, "mapping"},
		{"missing name", []string{"- result: pass"}, "name"},
		{"empty name", []string{"- name: ''", "  result: pass"}, "empty"},
		{"numeric name", []string{"- name: 12", "  result: pass"}, "string"},
		{"missing result", []string{"- name: /test"}, "result"},
		{"unknown status", []string{"- name: /test", "  result: unknown"}, "status"},
		{"null status", []string{"- name: /test", "  result: null"}, "result"},
		{"numeric status", []string{"- name: /test", "  result: 0"}, "string"},
		{"duplicate result", []string{"- name: /test", "  result: pass", "  result: fail"}, "duplicate"},
		{"bad field name", []string{"- name: /test", "  result: pass", "  7: unknown"}, "field names"},
		{"numeric duration", []string{"- name: /test", "  result: pass", "  duration: 3"}, "string"},
		{"scalar log", []string{"- name: /test", "  result: pass", "  log: file"}, "array"},
		{"null log", []string{"- name: /test", "  result: pass", "  log: null"}, "array"},
		{"numeric log path", []string{"- name: /test", "  result: pass", "  log: [12]"}, "strings"},
		{"empty log path", []string{"- name: /test", "  result: pass", "  log: ['']"}, "nonempty"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fs := testctx.NewCtx().FS()
			writeResultData(t, fs, "/native/plan/execute/results.yaml", test.lines...)
			entries, err := tmt.CollectResults(fs, "/native")
			require.ErrorContains(t, err, test.want)
			require.ErrorContains(t, err, "/native/plan/execute/results.yaml")
			assert.Nil(t, entries)
		})
	}
}

func TestCollectResultsDurationValidation(t *testing.T) {
	for _, value := range []string{
		"junk", "3s", "12", "-1:00:00", "1:60:00", "00:00:60", "00:00:NaN", "00:00:Inf",
		"1:2:3:4", "01:00:03suffix", "00:00:03.", "00:00:03.-1", "00::03", " 00:00:03",
		"00:00:+3", "00:00:3", "999999999999:00:00",
		"00:00:01.1234567890", "00:00:01.a", "00:00:01./", "00:00:01.:",
	} {
		t.Run(value, func(t *testing.T) {
			fs := testctx.NewCtx().FS()
			writeResultData(t, fs, "/native/plan/execute/results.yaml",
				"- name: /test", "  result: pass", "  duration: '"+value+"'",
			)
			_, err := tmt.CollectResults(fs, "/native")
			require.ErrorContains(t, err, "duration")
		})
	}

	fs := testctx.NewCtx().FS()
	writeResultData(t, fs, "/native/plan/execute/results.yaml",
		"- name: /long", "  result: pass", "  duration: '27:01:02.125'",
		"- name: /absent", "  result: skip",
	)
	entries, err := tmt.CollectResults(fs, "/native")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, 27*time.Hour+time.Minute+2125*time.Millisecond, entries[0].Duration)
	assert.Zero(t, entries[1].Duration)

	writeResultData(t, fs, "/native/plan/execute/results.yaml",
		"- name: /boundary", "  result: pass", "  duration: '00:59:59.012345679'",
	)
	entries, err = tmt.CollectResults(fs, "/native")
	require.NoError(t, err)
	assert.Equal(t, 59*time.Minute+59*time.Second+12345679*time.Nanosecond, entries[0].Duration)
}

func TestCollectResultsOutputContainment(t *testing.T) {
	for _, path := range []string{
		"../../../../outside", "/outside/log", "/native-other/log", `..\..\outside`, ".",
	} {
		t.Run(path, func(t *testing.T) {
			fs := testctx.NewCtx().FS()
			writeResultData(t, fs, "/native/plan/execute/results.yaml",
				"- name: /test", "  result: fail", "  log: ['"+path+"']",
			)
			entries, err := tmt.CollectResults(fs, "/native")
			require.Error(t, err)
			assert.Nil(t, entries)
		})
	}

	fs := testctx.NewCtx().FS()
	writeResultData(t, fs, "/native/plan/execute/results.yaml",
		"- name: /test", "  result: fail", "  log: [data/output.txt, ../../../../outside]",
	)
	_, err := tmt.CollectResults(fs, "/native")
	require.ErrorContains(t, err, "escapes", "all logs must be checked, not just the first")

	writeResultData(t, fs, "/native/plan/execute/results.yaml",
		"- name: /test", "  result: fail", "  log: [../shared/output.txt]",
	)
	entries, err := tmt.CollectResults(fs, "/native")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "/native/plan/shared/output.txt", entries[0].OutputPath)
}

type resultLinkInfo struct {
	os.FileInfo
}

func (info resultLinkInfo) Mode() os.FileMode {
	return info.FileInfo.Mode() | os.ModeSymlink
}

type resultLinkFS struct {
	opctx.FS
	link string
}

func (fs resultLinkFS) LstatIfPossible(path string) (os.FileInfo, bool, error) {
	info, err := fs.Stat(path)
	if err == nil && path == fs.link {
		return resultLinkInfo{FileInfo: info}, true, nil
	}

	return info, true, err
}

func TestCollectResultsRejectSymlinks(t *testing.T) {
	for _, link := range []string{
		"/native", "/native/plan", "/native/plan/execute/results.yaml",
		"/native/plan/execute/data", "/native/plan/execute/data/output.txt",
	} {
		t.Run(link, func(t *testing.T) {
			base := testctx.NewCtx().FS()
			writeResultData(t, base, "/native/plan/execute/results.yaml",
				"- name: /test", "  result: fail", "  log: [data/output.txt]",
			)
			writeResultData(t, base, "/native/plan/execute/data/output.txt", "failure")
			_, err := tmt.CollectResults(resultLinkFS{FS: base, link: link}, "/native")
			require.ErrorContains(t, err, "symlink")
		})
	}
}

type parsedJUnit struct {
	XMLName xml.Name `xml:"testsuites"`
	Suite   struct {
		Tests    int    `xml:"tests,attr"`
		Failures int    `xml:"failures,attr"`
		Errors   int    `xml:"errors,attr"`
		Skipped  int    `xml:"skipped,attr"`
		Time     string `xml:"time,attr"`
		Cases    []struct {
			Name    string  `xml:"name,attr"`
			Status  string  `xml:"status,attr"`
			Time    string  `xml:"time,attr"`
			Failure *string `xml:"failure"`
			Error   *string `xml:"error"`
			Skipped *string `xml:"skipped"`
		} `xml:"testcase"`
	} `xml:"testsuite"`
}

func TestWriteJUnitNativeStatusesAndCounts(t *testing.T) {
	fs := testctx.NewCtx().FS()
	statuses := []string{"pass", "fail", "error", "skip", "info", "warn", "pending"}

	lines := make([]string, 0, len(statuses)*3)
	for _, status := range statuses {
		lines = append(lines, "- name: '/tests/"+status+" & <case>'", "  result: "+status, "  duration: '00:00:01.5'")
	}

	writeResultData(t, fs, "/native/plan/execute/results.yaml", lines...)
	entries, err := tmt.CollectResults(fs, "/native")
	require.NoError(t, err)
	require.NoError(t, tmt.WriteJUnit(fs, "/reports/junit.xml", entries))
	raw, err := fileutils.ReadFile(fs, "/reports/junit.xml")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(raw), xml.Header))

	var report parsedJUnit
	require.NoError(t, xml.Unmarshal(raw, &report))
	assert.Equal(t, 7, report.Suite.Tests)
	assert.Equal(t, 1, report.Suite.Failures)
	assert.Equal(t, 3, report.Suite.Errors)
	assert.Equal(t, 2, report.Suite.Skipped)
	assert.Equal(t, "10.5", report.Suite.Time)
	require.Len(t, report.Suite.Cases, len(statuses))

	for index, testcase := range report.Suite.Cases {
		assert.Equal(t, "/tests/"+statuses[index]+" & <case>", testcase.Name)
		assert.Equal(t, statuses[index], testcase.Status)
		assert.Equal(t, "1.5", testcase.Time)

		switch testcase.Status {
		case "pass":
			assert.Nil(t, testcase.Failure)
			assert.Nil(t, testcase.Error)
			assert.Nil(t, testcase.Skipped)
		case "fail":
			assert.NotNil(t, testcase.Failure)
			assert.Nil(t, testcase.Error)
			assert.Nil(t, testcase.Skipped)
		case "error", "warn", "pending":
			assert.NotNil(t, testcase.Error)
			assert.Nil(t, testcase.Failure)
			assert.Nil(t, testcase.Skipped)
		case "info", "skip":
			assert.NotNil(t, testcase.Skipped)
			assert.Nil(t, testcase.Error)
			assert.Nil(t, testcase.Failure)
		}
	}
}

func TestWriteJUnitBoundedFailureOutput(t *testing.T) {
	fs := testctx.NewCtx().FS()
	writeResultData(t, fs, "/native/output.txt", "<failure & detail>"+strings.Repeat("a", 5000)+"DO NOT INCLUDE")

	entries := []tmt.ResultEntry{
		{Name: "/failed", Status: "fail", OutputPath: "/native/output.txt"},
		{Name: "/missing", Status: "error", OutputPath: "/native/missing.txt"},
	}
	require.NoError(t, tmt.WriteJUnit(fs, "/report.xml", entries))
	raw, err := fileutils.ReadFile(fs, "/report.xml")
	require.NoError(t, err)

	var report parsedJUnit
	require.NoError(t, xml.Unmarshal(raw, &report))
	require.Len(t, report.Suite.Cases, 2)
	require.NotNil(t, report.Suite.Cases[0].Failure)
	assert.Contains(t, *report.Suite.Cases[0].Failure, "Output: /native/output.txt")
	assert.Contains(t, *report.Suite.Cases[0].Failure, "<failure & detail>")
	assert.Contains(t, *report.Suite.Cases[0].Failure, "[output truncated]")
	assert.NotContains(t, *report.Suite.Cases[0].Failure, "DO NOT INCLUDE")
	assert.Less(t, len(*report.Suite.Cases[0].Failure), 4200)
	require.NotNil(t, report.Suite.Cases[1].Error)
	assert.Contains(t, *report.Suite.Cases[1].Error, "/native/missing.txt")
}

func TestWriteJUnitExactExcerptBoundary(t *testing.T) {
	for _, length := range []int{100, 4096, 4097} {
		fs := testctx.NewCtx().FS()
		content := strings.Repeat("x", length)
		require.NoError(t, fileutils.WriteFile(fs, "/output.txt", []byte(content), fileperms.PrivateFile))
		require.NoError(t, tmt.WriteJUnit(fs, "/report.xml", []tmt.ResultEntry{
			{Name: "failure", Status: "fail", OutputPath: "/output.txt"},
		}))
		data, err := fileutils.ReadFile(fs, "/report.xml")
		require.NoError(t, err)

		var report parsedJUnit
		require.NoError(t, xml.Unmarshal(data, &report))
		require.NotNil(t, report.Suite.Cases[0].Failure)

		expected := "Output: /output.txt\n" + content
		if length > 4096 {
			expected = "Output: /output.txt\n" + content[:4096] + "\n[output truncated]"
		}

		assert.Equal(t, expected, *report.Suite.Cases[0].Failure)
	}
}

func TestWriteJUnitEmptyAndInvalidEntries(t *testing.T) {
	fs := testctx.NewCtx().FS()
	require.NoError(t, tmt.WriteJUnit(fs, "/empty.xml", nil))
	raw, err := fileutils.ReadFile(fs, "/empty.xml")
	require.NoError(t, err)

	var report parsedJUnit
	require.NoError(t, xml.Unmarshal(raw, &report))
	assert.Zero(t, report.Suite.Tests)
	assert.Empty(t, report.Suite.Cases)

	for _, entry := range []tmt.ResultEntry{
		{Name: "/unknown", Status: "bogus"},
		{Name: "", Status: "pass"},
		{Name: "/negative", Status: "pass", Duration: -time.Second},
	} {
		err := tmt.WriteJUnit(fs, "/invalid.xml", []tmt.ResultEntry{entry})
		require.Error(t, err)
	}

	err = tmt.WriteJUnit(afero.NewReadOnlyFs(fs), "/denied.xml", nil)
	require.Error(t, err)
}
