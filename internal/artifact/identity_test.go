// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package artifact_test

import (
	"strings"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/artifact"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArtifactNamesAndContainerIdentities(t *testing.T) {
	for _, name := range []string{"", ".", "..", "/root", "../escape", "a/b", `a\b`, "nul\x00"} {
		require.Error(t, artifact.ValidateName(name))
	}

	require.NoError(t, artifact.ValidateName("candidate-name"))

	digest := strings.Repeat("a", 64)
	for _, input := range []string{digest, "sha256:" + digest, "\n" + strings.ToUpper(digest) + "\n"} {
		value, err := artifact.ParseContainerImageID(input)
		require.NoError(t, err)
		assert.Equal(t, "sha256:"+digest, value)
	}

	for _, input := range []string{
		"", "image:tag", strings.Repeat("a", 62), strings.Repeat("z", 64), digest + "\n" + digest,
	} {
		_, err := artifact.ParseContainerImageID(input)
		require.Error(t, err)
	}

	for _, input := range []string{digest, "sha256:" + strings.ToUpper(digest), "sha256:" + digest + "\n"} {
		require.Error(t, (artifact.Artifact{OCIImageID: input, Format: "oci"}).ValidateLocation())
	}

	require.NoError(t, (artifact.Artifact{Path: "/archive.oci.tar", Format: "oci"}).ValidateLocation())
}
