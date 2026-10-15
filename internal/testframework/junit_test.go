// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testframework_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/testctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeJUnitOutcomeMatrix(t *testing.T) {
	for _, test := range []struct {
		name, document string
		status         testframework.Status
		wantErr        bool
	}{
		{"pass", `<testsuite tests="1"><testcase name="pass" time="0.25"/></testsuite>`, testframework.Passed, false},
		{
			"skip", `<testsuite tests="1" skipped="1">` +
				`<testcase name="skip"><skipped message="not applicable"/></testcase></testsuite>`,
			testframework.Skipped, false,
		},
		{
			"fail", `<testsuite failures="1"><testcase name="fail"><failure/></testcase></testsuite>`,
			testframework.Failed, true,
		},
		{
			"error", `<testsuites><testsuite errors="1"><testcase name="error"><error/></testcase></testsuite></testsuites>`,
			testframework.Errored, true,
		},
		{"none", `<testsuites tests="0"/>`, testframework.Errored, true},
		{"mixed", `<testsuites tests="2"><testsuite><testcase name="skip"><skipped/></testcase></testsuite>` +
			`<testsuite><testcase name="pass"/></testsuite></testsuites>`, testframework.Passed, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := testctx.NewCtx()
			require.NoError(t, fileutils.WriteFile(ctx.FS(), "/native.xml", []byte(test.document), fileperms.PrivateFile))
			cases, err := testframework.ReadJUnit(ctx.FS(), "/native.xml")
			require.NoError(t, err)
			status, err := testframework.SummarizeCases(cases)
			assert.Equal(t, test.status, status)
			assert.Equal(t, test.wantErr, err != nil)

			if test.name == "pass" {
				assert.Equal(t, 250*time.Millisecond, cases[0].Duration)
			}

			if test.name == "skip" {
				assert.Equal(t, "not applicable", cases[0].Message)
			}
		})
	}
}

func TestJUnitRejectsMalformedOrContradictoryReports(t *testing.T) {
	for _, data := range []string{
		"<broken", "<other/>", `<testsuite tests="1"/>`,
		`<testsuite/><testsuite/>`, `<testsuite/>trailing`,
		`<testsuite failures="-1"><testcase name="test"/></testsuite>`,
		`<testsuite errors="1"><testcase name="test"/></testsuite>`,
		`<testsuite><testcase/></testsuite>`,
		`<testsuite><testcase name="test"><failure/><skipped/></testcase></testsuite>`,
		`<testsuite><testcase name="test" time="NaN"/></testsuite>`,
		`<testsuite><testcase name="test" time="Inf"/></testsuite>`,
		`<testsuite><testcase name="test" time="-1"/></testsuite>`,
		`<testsuite><testcase name="test" time="99999999999999999"/></testsuite>`,
	} {
		ctx := testctx.NewCtx()
		require.NoError(t, fileutils.WriteFile(ctx.FS(), "/native.xml", []byte(data), fileperms.PrivateFile))
		_, err := testframework.ReadJUnit(ctx.FS(), "/native.xml")
		require.Error(t, err, data)
	}
}

func TestResolveOutcomePreservesExecutionAndReportFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		native testframework.Status
		err    error
		cases  []testframework.CaseResult
		want   testframework.Status
	}{
		{
			"skip on success", testframework.Passed, nil,
			[]testframework.CaseResult{{Name: "skip", Status: testframework.Skipped}},
			testframework.Skipped,
		},
		{
			"false success", testframework.Passed, nil,
			[]testframework.CaseResult{{Name: "fail", Status: testframework.Failed}},
			testframework.Errored,
		},
		{
			"false failure", testframework.Failed, errors.New("exit 1"),
			[]testframework.CaseResult{{Name: "pass", Status: testframework.Passed}},
			testframework.Errored,
		},
		{
			"infrastructure dominates", testframework.Errored, errors.New("cleanup failed"),
			[]testframework.CaseResult{{Name: "fail", Status: testframework.Failed}},
			testframework.Errored,
		},
		{"cancelled", testframework.Cancelled, context.Canceled, nil, testframework.Cancelled},
		{"empty", testframework.Passed, nil, nil, testframework.Errored},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, err := testframework.ResolveOutcome(test.native, test.err, test.cases)
			assert.Equal(t, test.want, status)
			require.NoError(t, (testframework.RunResult{Status: status}).Validate(err))

			if test.err != nil {
				require.ErrorIs(t, err, test.err)
			}
		})
	}
}
