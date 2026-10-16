// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package magescenario

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2EOptionsMatchTargetSelections(t *testing.T) {
	for _, test := range []struct{ target, selector, timeout string }{
		{"all", "", "90m"},
		{"component", "^TestAzureLinuxComponent", "90m"},
		{"image", "^TestAzureLinuxImageBuild$", "25m"},
		{"frameworks", "^TestNativeFrameworkContracts$", "60m"},
	} {
		t.Run(test.target, func(t *testing.T) {
			options, err := e2eOptions(&test.target, nil)
			require.NoError(t, err)
			assert.Equal(t, test.selector, options.testSelector)
			assert.Equal(t, test.timeout, options.timeout)
			assert.Equal(t, "e2e", options.buildTag)
		})
	}

	options, err := e2eOptions(nil, nil)
	require.NoError(t, err)
	assert.Empty(t, options.testSelector, "default includes all E2E tests")
}

func TestE2EOptionsRejectBadSelectionsBeforeBuild(t *testing.T) {
	unknown, image, invalid, empty := "unknown", "image", "[", ""
	run := "^TestNativeFrameworkContracts$/local"
	_, err := e2eOptions(&unknown, nil)
	require.ErrorIs(t, err, ErrScenario)
	_, err = e2eOptions(&image, &run)
	require.Error(t, err)
	_, err = e2eOptions(nil, &invalid)
	require.Error(t, err)
	_, err = e2eOptions(nil, &empty)
	require.Error(t, err)
	options, err := e2eOptions(nil, &run)
	require.NoError(t, err)
	assert.Equal(t, run, options.testSelector)
	run = "^TestNativeFrameworkContracts$/local[/]fixture"
	_, err = e2eOptions(nil, &run)
	require.NoError(t, err, "slashes inside a character class are valid Go test patterns")
}
