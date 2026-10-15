// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package artifact_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/artifact"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/testctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingRenameFS struct{ opctx.FS }

func (failingRenameFS) Rename(string, string) error {
	return errors.New("injected rename failure")
}

func TestRecordPublishFailurePreservesPreviousManifest(t *testing.T) {
	ctx := testctx.NewCtx()
	record := artifact.BuildRecord{
		Name: "zlib-ng", Kind: "component", Arch: "x86_64",
		BuiltAt: time.Unix(1234, 0), Paths: []string{"/outputs/old.rpm"},
	}
	require.NoError(t, artifact.RecordBuild(ctx, "/output", record))
	path := artifact.RecordPath("/output", record.Kind, record.Name, record.Arch)
	before, err := fileutils.ReadFile(ctx.FS(), path)
	require.NoError(t, err)

	failure := testctx.NewCtx(testctx.WithFS(failingRenameFS{ctx.FS()}))
	record.Paths = []string{"/outputs/new.rpm"}
	require.ErrorContains(t, artifact.RecordBuild(failure, "/output", record), "injected rename")

	after, err := fileutils.ReadFile(ctx.FS(), path)
	require.NoError(t, err)
	assert.Equal(t, before, after)

	var saved artifact.BuildRecord
	require.NoError(t, json.Unmarshal(after, &saved))
	assert.Equal(t, []string{"/outputs/old.rpm"}, saved.Paths)

	staging, err := fileutils.Glob(ctx.FS(), "/output/build-records/component/zlib-ng/.record-*")
	require.NoError(t, err)
	assert.Empty(t, staging)
}
