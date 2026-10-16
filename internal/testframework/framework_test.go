// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testframework_test

import (
	"strings"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/artifact"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionValidation(t *testing.T) {
	tests := []struct {
		spec  testframework.ExecutionSpec
		valid bool
	}{
		{testframework.ExecutionSpec{Strategy: testframework.VM}, true},
		{testframework.ExecutionSpec{Strategy: testframework.Container}, true},
		{testframework.ExecutionSpec{Strategy: testframework.Host}, true},
		{testframework.ExecutionSpec{
			Strategy: testframework.SSH,
			SSH:      testframework.SSHConnection{Host: "guest", User: "tester"},
		}, true},
		{testframework.ExecutionSpec{Strategy: testframework.SSH}, false},
		{testframework.ExecutionSpec{Strategy: testframework.SSH, SSH: testframework.SSHConnection{Host: "guest"}}, false},
		{testframework.ExecutionSpec{Strategy: testframework.SSH, SSH: testframework.SSHConnection{User: "tester"}}, false},
		{testframework.ExecutionSpec{
			Strategy: testframework.SSH, SSH: testframework.SSHConnection{Host: "guest", User: "tester", Port: 65535},
		}, true},
		{testframework.ExecutionSpec{
			Strategy: testframework.SSH, SSH: testframework.SSHConnection{Host: "guest", User: "tester", Port: -1},
		}, false},
		{testframework.ExecutionSpec{
			Strategy: testframework.SSH, SSH: testframework.SSHConnection{Host: "guest", User: "tester", Port: 65536},
		}, false},
		{testframework.ExecutionSpec{Strategy: testframework.VM, SSH: testframework.SSHConnection{Host: "guest"}}, false},
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

func TestNativePathsPreserveInputIdentity(t *testing.T) {
	request := testframework.RunRequest{}
	assert.Empty(t, request.ImagePath())
	assert.Empty(t, request.RPMPaths())
	request.Image = &artifact.Artifact{Path: "/image", SHA256: "identity"}
	request.Packages = []artifact.Artifact{{Path: "/one.rpm"}, {Path: "/two.rpm"}}
	assert.Equal(t, "/image", request.ImagePath())
	paths := request.RPMPaths()
	assert.Equal(t, []string{"/one.rpm", "/two.rpm"}, paths)
	paths[0] = "/other"

	assert.Equal(t, "/one.rpm", request.Packages[0].Path)
	assert.Equal(t, "identity", request.Image.SHA256)
}

func TestMaterializedInputValidation(t *testing.T) {
	imageID := "sha256:" + strings.Repeat("a", 64)
	for _, request := range []testframework.RunRequest{
		{Image: &artifact.Artifact{Path: "relative"}},
		{Image: &artifact.Artifact{Path: "/file", OCIImageID: imageID, Format: "oci"}},
		{Packages: []artifact.Artifact{{Path: "relative", Format: "rpm"}}},
		{Packages: []artifact.Artifact{{Path: "/file", Format: "raw"}}},
		{Packages: []artifact.Artifact{{OCIImageID: imageID, Format: "rpm"}}},
	} {
		require.Error(t, request.ValidateInputs())
	}

	for _, request := range []testframework.RunRequest{
		{},
		{Image: &artifact.Artifact{Path: "/file", Format: "raw"}},
		{Image: &artifact.Artifact{OCIImageID: imageID, Format: "oci"}},
		{Packages: []artifact.Artifact{{Path: "/candidate.rpm", Format: "rpm"}}},
	} {
		require.NoError(t, request.ValidateInputs())
	}
}

func TestSSHConnectionRejectsUnsafeValues(t *testing.T) {
	for _, invalid := range []string{"-option", "user@host", "a/b", "a\x00b", "two words"} {
		require.Error(t, (testframework.SSHConnection{Host: invalid, User: "user"}).Validate())
		require.Error(t, (testframework.SSHConnection{Host: "host", User: invalid}).Validate())
	}

	require.Error(t, (testframework.SSHConnection{Host: "host", User: "user", KeyPath: "relative"}).Validate())
}
