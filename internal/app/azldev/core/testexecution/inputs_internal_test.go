// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testexecution

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/artifact"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/testctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOverlappingInvocationsOwnIndependentInputSnapshots(t *testing.T) {
	ctx := testctx.NewCtx()
	require.NoError(t, fileutils.WriteFile(ctx.FS(), "/pkg.rpm", []byte("first"), fileperms.PrivateFile))

	inputs := []testInputs{{Packages: []artifact.Artifact{{Path: "/pkg.rpm", Format: "rpm"}}}}
	inputs = append(inputs, inputs[0])
	first, cleanFirst, err := snapshotInputs(ctx, inputs)
	require.NoError(t, err)
	assert.Equal(t, first[0].Packages[0].Path, first[1].Packages[0].Path, "reuse only within one invocation")
	require.NoError(t, fileutils.WriteFile(ctx.FS(), "/pkg.rpm", []byte("rebuilt"), fileperms.PrivateFile))
	second, cleanSecond, err := snapshotInputs(ctx, inputs)
	require.NoError(t, err)
	assert.NotEqual(t, first[0].Packages[0].Path, second[0].Packages[0].Path)
	data, err := fileutils.ReadFile(ctx.FS(), first[0].Packages[0].Path)
	require.NoError(t, err)
	assert.Equal(t, "first", string(data))
	require.NoError(t, cleanFirst())

	data, err = fileutils.ReadFile(ctx.FS(), second[0].Packages[0].Path)
	require.NoError(t, err)
	assert.Equal(t, "rebuilt", string(data))
	require.NoError(t, cleanSecond())

	data, err = fileutils.ReadFile(ctx.FS(), "/pkg.rpm")
	require.NoError(t, err)
	assert.Equal(t, "rebuilt", string(data))
}

func TestInputPreparationFailureCleansItsPartialSnapshots(t *testing.T) {
	ctx := testctx.NewCtx()
	require.NoError(t, fileutils.WriteFile(ctx.FS(), "/pkg.rpm", []byte("original"), fileperms.PrivateFile))
	_, cleanup, err := snapshotInputs(ctx, []testInputs{{Packages: []artifact.Artifact{
		{Path: "/pkg.rpm", Format: "rpm"}, {Path: "/missing.rpm", Format: "rpm"},
	}}})
	require.Error(t, err)
	assert.Nil(t, cleanup)

	paths, err := fileutils.Glob(ctx.FS(), "/**/*")
	require.NoError(t, err)

	for _, path := range paths {
		assert.False(t, strings.HasPrefix(filepath.Base(path), "azldev-test-inputs-"), path)
	}

	data, err := fileutils.ReadFile(ctx.FS(), "/pkg.rpm")
	require.NoError(t, err)
	assert.Equal(t, "original", string(data))
}
