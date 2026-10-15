// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package artifacts_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/artifacts"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/artifact"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalImageReuseBuildAndForceRebuild(t *testing.T) {
	env := testutils.NewTestEnv(t)
	builds := 0

	resolver := artifacts.Resolver{Env: env.Env, BuildImage: func(name, arch string) ([]string, error) {
		builds++

		paths := []string{"/output/images/base/base.x86_64.raw", "/output/images/base/base.x86_64.qcow2"}
		for _, path := range paths {
			require.NoError(t, fileutils.WriteFile(env.TestFS, path, []byte("image"), fileperms.PrivateFile))
		}

		require.NoError(t, artifact.RecordBuild(env.Env, env.Env.OutputDir(), artifact.BuildRecord{
			Name: name, Kind: "image", Arch: arch, BuiltAt: time.Now(), Paths: paths,
		}))

		return paths, nil
	}}
	for _, force := range []bool{false, false, true} {
		got, err := resolver.Image("base", "x86_64", []string{"qcow2"}, artifacts.Selection{Rebuild: force},
			projectconfig.DistroVersionDefinition{})
		require.NoError(t, err)
		assert.Equal(t, "/output/images/base/base.x86_64.qcow2", got.Path)
		assert.False(t, got.BuiltAt.IsZero())
	}

	assert.Equal(t, 2, builds)
}

func TestFailedBuildDoesNotFallBack(t *testing.T) {
	env := testutils.NewTestEnv(t)
	buildErr := errors.New("build failed")
	resolver := artifacts.Resolver{Env: env.Env, BuildImage: func(_, _ string) ([]string, error) {
		return nil, buildErr
	}}
	_, err := resolver.Image("base", "x86_64", []string{"qcow2"},
		artifacts.Selection{}, projectconfig.DistroVersionDefinition{})
	require.ErrorIs(t, err, buildErr)
}

func TestExplicitPathsAndAmbiguousImages(t *testing.T) {
	env := testutils.NewTestEnv(t)

	resolver := artifacts.Resolver{Env: env.Env}
	for _, path := range []string{"/project/one.qcow2", "/project/two.qcow2"} {
		require.NoError(t, fileutils.WriteFile(env.TestFS, path, []byte("image"), fileperms.PrivateFile))
	}

	got, err := resolver.Image("base", "x86_64", []string{"qcow2"}, artifacts.Selection{Paths: []string{"one.qcow2"}},
		projectconfig.DistroVersionDefinition{})
	require.NoError(t, err)
	assert.Equal(t, "/project/one.qcow2", got.Path)

	_, err = resolver.Image("base", "x86_64", []string{"qcow2"}, artifacts.Selection{Paths: []string{"*.qcow2"}},
		projectconfig.DistroVersionDefinition{})
	require.ErrorContains(t, err, "ambiguous")
}

type fakeDownloader struct {
	env     *testutils.TestEnv
	count   int
	content string
}

func (d *fakeDownloader) Download(_ context.Context, _ string, path string) error {
	d.count++

	content := d.content
	if content == "" {
		content = "image"
	}

	return fileutils.WriteFile(d.env.TestFS, path, []byte(content), fileperms.PrivateFile)
}

func (*fakeDownloader) FetchStream(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("unexpected stream request")
}

func TestPublishedFormatDoesNotDependOnURLSuffix(t *testing.T) {
	env := testutils.NewTestEnv(t)
	resolver := artifacts.Resolver{Env: env.Env, Downloader: &fakeDownloader{env: env}}
	distro := projectconfig.DistroVersionDefinition{Images: map[string]projectconfig.PublishedImage{
		"base": {Architectures: map[string][]projectconfig.PublishedImageArtifact{
			"x86_64": {{Format: "qcow2", URI: "https://example.test/download?build=123"}},
		}},
	}}
	got, err := resolver.Image("base", "x86_64", []string{"qcow2"}, artifacts.Selection{Source: "lkg"}, distro)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(got.Path, ".qcow2"), got.Path)
}

func TestRefreshingLKGDoesNotReplaceEarlierRunInput(t *testing.T) {
	env := testutils.NewTestEnv(t)
	download := &fakeDownloader{env: env, content: "first image"}
	resolver := artifacts.Resolver{Env: env.Env, Downloader: download}
	distro := projectconfig.DistroVersionDefinition{Images: map[string]projectconfig.PublishedImage{
		"base": {Architectures: map[string][]projectconfig.PublishedImageArtifact{
			"x86_64": {{Format: "qcow2", URI: "https://example.test/lkg.qcow2"}},
		}},
	}}
	first, err := resolver.Image("base", "x86_64", []string{"qcow2"}, artifacts.Selection{Source: "lkg"}, distro)
	require.NoError(t, err)

	download.content = "new image"
	second, err := resolver.Image("base", "x86_64", []string{"qcow2"}, artifacts.Selection{Source: "lkg"}, distro)
	require.NoError(t, err)
	assert.NotEqual(t, first.Path, second.Path)
	content, err := fileutils.ReadFile(env.TestFS, first.Path)
	require.NoError(t, err)
	assert.Equal(t, "first image", string(content))
}

func TestPublishedImageResolutionAndDigest(t *testing.T) {
	env := testutils.NewTestEnv(t)
	download := &fakeDownloader{env: env}
	resolver := artifacts.Resolver{Env: env.Env, Downloader: download}

	distro := projectconfig.DistroVersionDefinition{Images: map[string]projectconfig.PublishedImage{
		"base": {Architectures: map[string][]projectconfig.PublishedImageArtifact{
			"x86_64": {{Format: "qcow2", URI: "https://example.test/lkg.qcow2"}},
		}},
	}}
	for range 2 {
		got, err := resolver.Image("base", "x86_64", []string{"qcow2"}, artifacts.Selection{Source: "lkg"}, distro)
		require.NoError(t, err)
		assert.NotEmpty(t, got.SHA256)
		assert.True(t, got.BuiltAt.IsZero())
	}

	assert.Equal(t, 2, download.count, "mutable LKG locations must be refreshed")

	distro.Images["base"].Architectures["x86_64"][0].SHA256 = strings.Repeat("0", 64)
	_, err := resolver.Image("base", "x86_64", []string{"qcow2"}, artifacts.Selection{Source: "lkg"}, distro)
	require.ErrorContains(t, err, "SHA-256 mismatch")
	_, err = resolver.Image("base", "aarch64", []string{"qcow2"}, artifacts.Selection{Source: "lkg"}, distro)
	require.ErrorContains(t, err, "no compatible")
}

func TestSelectionConflictsAndAge(t *testing.T) {
	for _, selection := range []artifacts.Selection{
		{Source: "unknown"},
		{Source: "path"},
		{Source: "lkg", Rebuild: true},
		{Paths: []string{"file"}, Rebuild: true},
		{Paths: []string{"file"}, Source: "lkg"},
	} {
		require.Error(t, selection.Validate())
	}

	assert.Equal(t, "3 days ago", artifacts.HumanAge(73*time.Hour))
	assert.Equal(t, "1 minute ago", artifacts.HumanAge(90*time.Second))
	assert.Equal(t, "timestamp is in the future", artifacts.HumanAge(-time.Second))
}

func TestCompletePackageBuildIsReusedAndMissingOutputsRebuild(t *testing.T) {
	env := testutils.NewTestEnv(t)
	builds := 0
	paths := []string{"/output/rpms/base/curl-1-1.x86_64.rpm", "/output/rpms/base/libcurl-1-1.x86_64.rpm"}

	resolver := artifacts.Resolver{Env: env.Env, BuildPackages: func(name, arch string) ([]string, error) {
		builds++

		for _, path := range paths {
			require.NoError(t, fileutils.WriteFile(env.TestFS, path, []byte("rpm"), fileperms.PrivateFile))
		}

		require.NoError(t, artifact.RecordBuild(env.Env, env.Env.OutputDir(), artifact.BuildRecord{
			Name: name, Kind: "component", Arch: arch, BuiltAt: time.Now(), Paths: paths,
		}))

		return paths, nil
	}}
	for range 2 {
		got, err := resolver.Packages("curl", "x86_64", artifacts.Selection{}, projectconfig.DistroVersionDefinition{})
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, paths[1], got[1].Path)
	}

	assert.Equal(t, 1, builds)
	require.NoError(t, env.TestFS.Remove(paths[1]))

	_, err := resolver.Packages("curl", "x86_64", artifacts.Selection{}, projectconfig.DistroVersionDefinition{})
	require.NoError(t, err)
	assert.Equal(t, 2, builds)
}

func TestRecordRejectsPathTraversal(t *testing.T) {
	env := testutils.NewTestEnv(t)
	require.Error(t, artifact.RecordBuild(env.Env, "/output", artifact.BuildRecord{
		Name: "../outside", Kind: "component", Arch: "x86_64", Paths: []string{"/pkg.rpm"},
	}))
}
