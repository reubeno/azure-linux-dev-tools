// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testexecution

import (
	"context"
	"errors"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type resultRunner struct {
	result testframework.RunResult
	err    error
}

func (r resultRunner) Requirements(testframework.ExecutionSpec) (testframework.Requirements, error) {
	return testframework.Requirements{}, nil
}

func (r resultRunner) Run(opctx.Ctx, testframework.RunRequest) (testframework.RunResult, error) {
	return r.result, r.err
}

func TestExecutionHonorsAdapterResultContract(t *testing.T) {
	for _, test := range []struct {
		name          string
		status        testframework.Status
		runErr        error
		want          testframework.Status
		contractError bool
	}{
		{"unspecified is not success", "", nil, testframework.Errored, true},
		{"failure requires an error", testframework.Failed, nil, testframework.Errored, true},
		{"success cannot hide error", testframework.Passed, errors.New("error"), testframework.Errored, true},
		{
			"cleanup error dominates assertions", testframework.Errored,
			errors.Join(errors.New("cleanup"), testframework.ErrTestFailed),
			testframework.Errored, false,
		},
		{"native cancellation retained", testframework.Cancelled, context.Canceled, testframework.Cancelled, false},
		{"skip reason retained", testframework.Skipped, nil, testframework.Skipped, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := testutils.NewTestEnv(t)
			native := testframework.RunResult{Status: test.status, Error: "native reason", Cases: []testframework.CaseResult{
				{Name: "native case", Status: "skip"},
			}}
			plan := plannedTest{runner: resultRunner{result: native, err: test.runErr}}
			plan.test.Name = "test"
			got, err := executeOne(env.Env, Subject{Name: "subject"}, plan, testInputs{}, Options{}, "/work/run", false)
			assert.Equal(t, test.want, got.Status)
			assert.Equal(t, native.Cases, got.Cases)

			switch {
			case test.contractError:
				require.ErrorContains(t, err, "invalid framework result")
			case test.runErr != nil:
				require.ErrorIs(t, err, test.runErr)
			default:
				require.NoError(t, err)
				assert.Equal(t, "native reason", got.Error)
			}
		})
	}
}
