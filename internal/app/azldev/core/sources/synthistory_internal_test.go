// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sources

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilterFingerprintChangesAfterBaseline(t *testing.T) {
	changes := []FingerprintChange{
		{InputFingerprint: "sha256:first"},
		{InputFingerprint: "sha256:baseline"},
		{InputFingerprint: "sha256:migration"},
		{InputFingerprint: "sha256:bump"},
	}

	filtered, err := filterFingerprintChangesAfterBaseline(changes, "sha256:baseline")
	require.NoError(t, err)
	require.Len(t, filtered, 2)
	assert.Equal(t, "sha256:migration", filtered[0].InputFingerprint)
	assert.Equal(t, "sha256:bump", filtered[1].InputFingerprint)

	unfiltered, err := filterFingerprintChangesAfterBaseline(changes, "")
	require.NoError(t, err)
	assert.Equal(t, changes, unfiltered)

	_, err = filterFingerprintChangesAfterBaseline(changes, "sha256:missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "was not found")
}
