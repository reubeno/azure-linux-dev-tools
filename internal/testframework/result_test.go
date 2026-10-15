// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testframework_test

import (
	"context"
	"errors"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/stretchr/testify/require"
)

func TestResultContract(t *testing.T) {
	infrastructure := errors.New("cleanup failed")
	for _, test := range []struct {
		name   string
		status testframework.Status
		err    error
		valid  bool
	}{
		{"passed", testframework.Passed, nil, true},
		{"skipped", testframework.Skipped, nil, true},
		{"planned", testframework.Planned, nil, true},
		{"failed", testframework.Failed, testframework.ErrTestFailed, true},
		{
			"infrastructure after test failure", testframework.Errored,
			errors.Join(infrastructure, testframework.ErrTestFailed), true,
		},
		{"cancelled", testframework.Cancelled, context.Canceled, true},
		{"unspecified", "", nil, false},
		{"unknown", "unknown", nil, false},
		{"passed with error", testframework.Passed, infrastructure, false},
		{"skipped with error", testframework.Skipped, infrastructure, false},
		{"planned with error", testframework.Planned, infrastructure, false},
		{"failed without sentinel", testframework.Failed, infrastructure, false},
		{"failed without error", testframework.Failed, nil, false},
		{"infrastructure without error", testframework.Errored, nil, false},
		{"cancelled without error", testframework.Cancelled, nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := (testframework.RunResult{Status: test.status}).Validate(test.err)
			if test.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
