// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cargovendor_test

import (
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/components/components_testutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/providers/sourcegen"
	"github.com/microsoft/azure-linux-dev-tools/internal/providers/sourcegen/cargovendor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func newTestCtx(t *testing.T) *testutils.TestEnv {
	t.Helper()

	return testutils.NewTestEnv(t)
}

func TestResolveCrateName(t *testing.T) {
	tests := []struct {
		name          string
		componentName string
		crateName     string // explicit override in origin
		expected      string
	}{
		{
			name:          "explicit crate-name takes highest priority",
			componentName: "rust-ripgrep",
			crateName:     "custom-crate",
			expected:      "custom-crate",
		},
		{
			name:          "strip rust- prefix when present",
			componentName: "rust-ripgrep",
			expected:      "ripgrep",
		},
		{
			name:          "strip rust- prefix for complex name",
			componentName: "rust-serde-json",
			expected:      "serde-json",
		},
		{
			name:          "no prefix to strip, use component name as-is",
			componentName: "ripgrep",
			expected:      "ripgrep",
		},
		{
			name:          "rusty prefix is not stripped (only rust- is)",
			componentName: "rusty-tool",
			expected:      "rusty-tool",
		},
		{
			name:          "explicit crate-name overrides even without rust- prefix",
			componentName: "mypackage",
			crateName:     "my-actual-crate",
			expected:      "my-actual-crate",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)

			mockComponent := components_testutils.NewMockComponent(ctrl)
			mockComponent.EXPECT().GetName().AnyTimes().Return(testCase.componentName)

			request := &sourcegen.GenerateRequest{
				Component: mockComponent,
				Origin: projectconfig.Origin{
					CrateName: testCase.crateName,
				},
			}

			result := cargovendor.ResolveCrateName(request)
			assert.Equal(t, testCase.expected, result)
		})
	}
}

func TestCheckPrerequisites(t *testing.T) {
	t.Run("rust2rpm available", func(t *testing.T) {
		testEnv := newTestCtx(t)
		testEnv.CmdFactory.RegisterCommandInSearchPath("rust2rpm")

		gen := cargovendor.New(testEnv.Env)

		err := gen.CheckPrerequisites(testEnv.Env)
		assert.NoError(t, err)
	})

	t.Run("rust2rpm not available", func(t *testing.T) {
		testEnv := newTestCtx(t)

		gen := cargovendor.New(testEnv.Env)

		err := gen.CheckPrerequisites(testEnv.Env)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rust2rpm")
	})
}
