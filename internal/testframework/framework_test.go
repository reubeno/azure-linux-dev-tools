// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testframework_test

import (
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/stretchr/testify/require"
)

func TestExecutionValidation(t *testing.T) {
	tests := []struct {
		spec testframework.ExecutionSpec
		valid bool
	}{
		{testframework.ExecutionSpec{Strategy: testframework.VM}, true},
		{testframework.ExecutionSpec{Strategy: testframework.Container}, true},
		{testframework.ExecutionSpec{Strategy: testframework.Host}, true},
		{testframework.ExecutionSpec{Strategy: testframework.SSH, SSHHost: "guest", SSHUser: "tester"}, true},
		{testframework.ExecutionSpec{Strategy: testframework.SSH}, false},
		{testframework.ExecutionSpec{Strategy: testframework.SSH, SSHHost: "guest", SSHUser: "tester", SSHPort: 65536}, false},
		{testframework.ExecutionSpec{Strategy: testframework.VM, SSHHost: "guest"}, false},
		{testframework.ExecutionSpec{Strategy: "unknown"}, false},
	}
	for _, test := range tests {
		if test.valid {
			require.NoError(t, test.spec.Validate())
		} else {
			require.Error(t, test.spec.Validate())
		}
	}
}

func TestManagedNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../source", "/source", `a\b`} {
		require.Error(t, testframework.ValidateName(name))
	}
	require.NoError(t, testframework.ValidateName("kernel-smoke"))
}
