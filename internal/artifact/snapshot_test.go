// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package artifact_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/artifact"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/testctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotsSurviveRebuildsAndRemainIndependent(t *testing.T) {
	ctx := testctx.NewCtx()
	require.NoError(t, fileutils.WriteFile(ctx.FS(), "/pkg.rpm", []byte("first"), fileperms.PrivateFile))

	input := artifact.Artifact{Path: "/pkg.rpm", Format: "rpm", Source: "local", Arch: "x86_64", BuiltAt: time.Now()}
	first, err := artifact.Snapshot(ctx, input, "/runs/first")
	require.NoError(t, err)
	require.NoError(t, fileutils.WriteFile(ctx.FS(), "/pkg.rpm", []byte("second"), fileperms.PrivateFile))
	second, err := artifact.Snapshot(ctx, input, "/runs/second")
	require.NoError(t, err)
	assert.NotEqual(t, first.Path, second.Path)
	assert.NotEqual(t, first.SHA256, second.SHA256)
	assert.Equal(t, "/pkg.rpm", first.OriginalPath)
	assert.Equal(t, input.BuiltAt, first.BuiltAt)
	data, err := fileutils.ReadFile(ctx.FS(), first.Path)
	require.NoError(t, err)
	assert.Equal(t, "first", string(data))

	info, err := ctx.FS().Stat(first.Path)
	require.NoError(t, err)
	assert.Equal(t, fileperms.PrivateReadOnlyFile, info.Mode().Perm())
	require.NoError(t, ctx.FS().RemoveAll("/runs/second"))
	_, err = ctx.FS().Stat(first.Path)
	require.NoError(t, err)
}

func TestSnapshotRejectsChangesAndKeepsUnownedDirectories(t *testing.T) {
	for _, input := range []artifact.Artifact{
		{Path: "/pkg", ModifiedAt: time.Unix(1, 0)},
		{Path: "/pkg", SHA256: strings.Repeat("0", 64)},
		{Path: "/missing"},
	} {
		ctx := testctx.NewCtx()
		require.NoError(t, fileutils.WriteFile(ctx.FS(), "/pkg", []byte("data"), fileperms.PrivateFile))
		_, err := artifact.Snapshot(ctx, input, "/run/new")
		require.Error(t, err)
		exists, err := fileutils.Exists(ctx.FS(), "/run/new")
		require.NoError(t, err)
		assert.False(t, exists)
	}

	ctx := testctx.NewCtx()
	require.NoError(t, fileutils.MkdirAll(ctx.FS(), "/unowned"))
	require.NoError(t, fileutils.WriteFile(ctx.FS(), "/unowned/keep", []byte("keep"), fileperms.PrivateFile))
	_, err := artifact.Snapshot(ctx, artifact.Artifact{Path: "/pkg"}, "/unowned")
	require.Error(t, err)
	data, err := fileutils.ReadFile(ctx.FS(), "/unowned/keep")
	require.NoError(t, err)
	assert.Equal(t, "keep", string(data))
}

type changingFS struct {
	opctx.FS
	reads  int
	cancel context.CancelFunc
}
type changingFile struct {
	opctx.File
	fs *changingFS
}

func (fs *changingFS) Open(name string) (afero.File, error) {
	file, err := fs.FS.Open(name)
	if err != nil {
		return nil, err
	}

	return &changingFile{File: file, fs: fs}, nil
}

func (file *changingFile) Stat() (os.FileInfo, error) {
	file.fs.reads++
	if file.fs.reads == 2 && file.fs.cancel == nil {
		if err := file.fs.Chtimes(file.Name(), time.Unix(2, 0), time.Unix(2, 0)); err != nil {
			return nil, err
		}
	}

	return file.File.Stat()
}

func (file *changingFile) Read(data []byte) (int, error) {
	count, err := file.File.Read(data)
	if file.fs.cancel != nil {
		file.fs.cancel()
	}

	return count, err
}

func TestSnapshotDetectsConcurrentInputMutation(t *testing.T) {
	base := testctx.NewCtx()
	require.NoError(t, fileutils.WriteFile(base.FS(), "/input", []byte("bytes"), fileperms.PrivateFile))
	fs := &changingFS{FS: base.FS()}
	ctx := testctx.NewCtx(testctx.WithFS(fs))
	_, err := artifact.Snapshot(ctx, artifact.Artifact{Path: "/input"}, "/run/input")
	require.ErrorContains(t, err, "changed during snapshot")
	exists, err := fileutils.Exists(base.FS(), "/run/input")
	require.NoError(t, err)
	assert.False(t, exists)
}

type cleanupFailureFS struct {
	opctx.FS
	failure error
}

func (fs cleanupFailureFS) RemoveAll(string) error { return fs.failure }

func TestSnapshotReportsPartialCleanupFailure(t *testing.T) {
	base := testctx.NewCtx()
	require.NoError(t, fileutils.WriteFile(base.FS(), "/input", []byte("data"), fileperms.PrivateFile))

	cleanupErr := errors.New("cleanup denied")
	ctx := testctx.NewCtx(testctx.WithFS(cleanupFailureFS{FS: base.FS(), failure: cleanupErr}))
	_, err := artifact.Snapshot(ctx, artifact.Artifact{Path: "/input", SHA256: strings.Repeat("0", 64)}, "/snapshot")
	require.ErrorContains(t, err, "SHA-256 mismatch")
	require.ErrorIs(t, err, cleanupErr)
}

func TestSnapshotIdentityAndCancellation(t *testing.T) {
	ctx := testctx.NewCtx()
	imageID := artifact.Artifact{OCIImageID: "sha256:" + strings.Repeat("a", 64), Format: "oci"}
	got, err := artifact.Snapshot(ctx, imageID, "/unused")
	require.NoError(t, err)
	assert.Equal(t, imageID, got)

	for _, bad := range []artifact.Artifact{
		{Path: "relative"},
		{OCIImageID: imageID.OCIImageID, Path: "/file", Format: "oci"},
		{OCIImageID: "tag:latest", Format: "oci"},
		{OCIImageID: imageID.OCIImageID, Format: "raw"},
	} {
		require.Error(t, bad.ValidateLocation())
	}

	lifetime, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = artifact.Snapshot(opctx.WithContext(ctx, lifetime), imageID, "/unused")
	require.ErrorIs(t, err, context.Canceled)
	exists, err := fileutils.Exists(ctx.FS(), "/unused")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestSnapshotCancellationRemovesPartialFiles(t *testing.T) {
	base := testctx.NewCtx()
	require.NoError(t, fileutils.WriteFile(base.FS(), "/input", []byte("bytes"), fileperms.PrivateFile))

	lifetime, cancel := context.WithCancel(t.Context())
	defer cancel()

	fs := &changingFS{FS: base.FS(), cancel: cancel}
	ctx := testctx.NewCtx(testctx.WithFS(fs))
	_, err := artifact.Snapshot(opctx.WithContext(ctx, lifetime), artifact.Artifact{Path: "/input"}, "/run/input")
	require.ErrorIs(t, err, context.Canceled)
	exists, err := fileutils.Exists(base.FS(), "/run/input")
	require.NoError(t, err)
	assert.False(t, exists)
}
