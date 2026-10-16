// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package artifacts_test

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/artifacts"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/artifact"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmptyPackageBuildCannotProduceSuccessfulInputs(t *testing.T) {
	env := testutils.NewTestEnv(t)
	resolver := artifacts.Resolver{Env: env.Env, BuildPackages: func(string, string) ([]string, error) { return nil, nil }}
	_, err := resolver.Packages("pkg", "x86_64", artifacts.Selection{}, projectconfig.DistroVersionDefinition{})
	require.ErrorContains(t, err, "no binary RPM")
}

func TestLegacyPackagesUseHeadersRatherThanFilenames(t *testing.T) {
	env := testutils.NewTestEnv(t)
	env.CmdFactory.RegisterCommandInSearchPath("rpm")

	headers := map[string]string{
		"unrelated-name.rpm": "zlib-ng-1-1.src.rpm\tx86_64",
		"data.rpm":           "zlib-ng-1-1.src.rpm\tnoarch",
		"foreign.rpm":        "other-1-1.src.rpm\tx86_64",
		"wrong-arch.rpm":     "zlib-ng-1-1.src.rpm\taarch64",
		"malformed-name.rpm": "invalid\tx86_64",
		"source.src.rpm":     "",
	}
	for name := range headers {
		require.NoError(t, fileutils.WriteFile(env.TestFS, "/output/rpms/repo/"+name, []byte("rpm"), fileperms.PrivateFile))
	}

	env.CmdFactory.RunAndGetOutputHandler = func(cmd *exec.Cmd) (string, error) {
		assert.Equal(t, []string{"rpm", "-qp", "--qf", "%{SOURCERPM}\\t%{ARCH}"}, cmd.Args[:4])
		name := filepath.Base(cmd.Args[4])
		assert.NotEqual(t, "source.src.rpm", name)

		return headers[name], nil
	}
	resolver := artifacts.Resolver{Env: env.Env}
	got, err := resolver.Packages("zlib-ng", "x86_64", artifacts.Selection{}, projectconfig.DistroVersionDefinition{})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.ElementsMatch(t, []string{"/output/rpms/repo/unrelated-name.rpm", "/output/rpms/repo/data.rpm"},
		[]string{got[0].Path, got[1].Path})

	headers["data.rpm"] = "zlib-ng-2-1.src.rpm\tnoarch"
	_, err = resolver.Packages("zlib-ng", "x86_64", artifacts.Selection{}, projectconfig.DistroVersionDefinition{})
	require.ErrorContains(t, err, "multiple local builds")
}

func TestLegacyPackageQueryErrorsAreNotBuildFallbacks(t *testing.T) {
	for _, output := range []string{"invalid", "source.src.rpm\t", "\tx86_64"} {
		env := testutils.NewTestEnv(t)
		env.CmdFactory.RegisterCommandInSearchPath("rpm")
		require.NoError(t, fileutils.WriteFile(env.TestFS, "/output/rpms/repo/pkg.rpm", nil, fileperms.PrivateFile))
		env.CmdFactory.RunAndGetOutputHandler = func(*exec.Cmd) (string, error) { return output, nil }
		resolver := artifacts.Resolver{Env: env.Env, BuildPackages: func(string, string) ([]string, error) {
			t.Fatal("query errors must not trigger a build")

			return nil, nil
		}}
		_, err := resolver.Packages("pkg", "x86_64", artifacts.Selection{}, projectconfig.DistroVersionDefinition{})
		require.ErrorContains(t, err, "unexpected RPM identity")

		nativeErr := errors.New("query failed")
		env.CmdFactory.RunAndGetOutputHandler = func(cmd *exec.Cmd) (string, error) {
			_, err := cmd.Stderr.Write([]byte("RPM diagnostic"))
			require.NoError(t, err)

			return "", nativeErr
		}
		_, err = resolver.Packages("pkg", "x86_64", artifacts.Selection{}, projectconfig.DistroVersionDefinition{})
		require.ErrorIs(t, err, nativeErr)
		require.ErrorContains(t, err, "RPM diagnostic")
	}
}

func TestBuildRecordsRejectMismatchedIdentityAndRelativePaths(t *testing.T) {
	for _, record := range []artifact.BuildRecord{
		{Name: "other", Kind: "image", Arch: "x86_64", Paths: []string{"/base.raw"}},
		{Name: "base", Kind: "component", Arch: "x86_64", Paths: []string{"/base.raw"}},
		{Name: "base", Kind: "image", Arch: "aarch64", Paths: []string{"/base.raw"}},
		{Name: "base", Kind: "image", Arch: "x86_64"},
		{Name: "base", Kind: "image", Arch: "x86_64", Paths: []string{"relative.raw"}},
	} {
		env := testutils.NewTestEnv(t)
		data, err := json.Marshal(record)
		require.NoError(t, err)
		require.NoError(t, fileutils.WriteFile(env.TestFS, "/base.raw", []byte("image"), fileperms.PrivateFile))

		path := artifact.RecordPath("/output", "image", "base", "x86_64")
		require.NoError(t, fileutils.WriteFile(env.TestFS, path, data, fileperms.PrivateFile))
		resolver := artifacts.Resolver{Env: env.Env}
		_, err = resolver.Image("base", "x86_64", []string{"raw"}, artifacts.Selection{},
			projectconfig.DistroVersionDefinition{})
		require.Error(t, err)
	}
}

func TestExplicitCandidatesMustBeBinaryRPMs(t *testing.T) {
	for _, path := range []string{"/source.src.rpm", "/file.txt"} {
		env := testutils.NewTestEnv(t)
		require.NoError(t, fileutils.WriteFile(env.TestFS, path, []byte("bytes"), fileperms.PrivateFile))
		resolver := artifacts.Resolver{Env: env.Env}
		_, err := resolver.Packages("pkg", "x86_64", artifacts.Selection{Paths: []string{path}},
			projectconfig.DistroVersionDefinition{})
		require.ErrorContains(t, err, "binary RPM")
	}
}

func TestVerifiedCacheReuseAndCorruptionDetection(t *testing.T) {
	env := testutils.NewTestEnv(t)
	download := &fakeDownloader{env: env}
	resolver := artifacts.Resolver{Env: env.Env, Downloader: download}
	distro := projectconfig.DistroVersionDefinition{Images: map[string]projectconfig.PublishedImage{
		"base": {Architectures: map[string][]projectconfig.PublishedImageArtifact{
			"x86_64": {{Format: "qcow2", URI: "https://example.test/download"}},
		}},
	}}
	first, err := resolver.Image("base", "x86_64", []string{"qcow2"}, artifacts.Selection{Source: "lkg"}, distro)
	require.NoError(t, err)

	distro.Images["base"].Architectures["x86_64"][0].SHA256 = first.SHA256
	second, err := resolver.Image("base", "x86_64", []string{"qcow2"}, artifacts.Selection{Source: "lkg"}, distro)
	require.NoError(t, err)
	assert.Equal(t, first.Path, second.Path)
	assert.Equal(t, 1, download.count)
	require.NoError(t, fileutils.WriteFile(env.TestFS, first.Path, []byte("corrupt"), fileperms.PrivateFile))

	_, err = resolver.Image("base", "x86_64", []string{"qcow2"}, artifacts.Selection{Source: "lkg"}, distro)
	require.ErrorContains(t, err, "cached artifact SHA-256 mismatch")
	assert.Equal(t, 1, download.count, "corruption must not be silently hidden by redownloading")
}

func TestInvalidDownloadLocationsAndDigestsFailBeforeTransfer(t *testing.T) {
	for _, entry := range []projectconfig.PublishedImageArtifact{
		{Format: "raw", URI: ""},
		{Format: "raw", URI: "file:///image"},
		{Format: "raw", URI: "ftp://example.test/image"},
		{Format: "raw", URI: "https:///missing-host"},
		{Format: "raw", URI: "https://[invalid"},
		{Format: "raw", URI: "https://example.test/image", SHA256: "invalid"},
		{Format: "raw", URI: "https://example.test/image", SHA256: strings.Repeat("a", 62)},
	} {
		env := testutils.NewTestEnv(t)
		download := &fakeDownloader{env: env}
		resolver := artifacts.Resolver{Env: env.Env, Downloader: download}
		distro := projectconfig.DistroVersionDefinition{Images: map[string]projectconfig.PublishedImage{
			"base": {Architectures: map[string][]projectconfig.PublishedImageArtifact{"x86_64": {entry}}},
		}}
		_, err := resolver.Image("base", "x86_64", []string{"raw"}, artifacts.Selection{Source: "lkg"}, distro)
		require.Error(t, err)
		assert.Zero(t, download.count)
	}
}

type failingDownload struct {
	*fakeDownloader
	failure error
}

func (download failingDownload) Download(ctx context.Context, uri, path string) error {
	if strings.Contains(uri, "fail") {
		return download.failure
	}

	return download.fakeDownloader.Download(ctx, uri, path)
}

func TestPublishedPackageSetsNeverReturnPartialInputs(t *testing.T) {
	env := testutils.NewTestEnv(t)
	nativeErr := errors.New("download failed")
	download := failingDownload{fakeDownloader: &fakeDownloader{env: env}, failure: nativeErr}
	resolver := artifacts.Resolver{Env: env.Env, Downloader: download}
	distro := projectconfig.DistroVersionDefinition{TestPackages: map[string]projectconfig.PublishedPackages{
		"pkg": {Architectures: map[string][]projectconfig.PublishedPackageArtifact{
			"x86_64": {{URI: "https://example.test/one"}, {URI: "https://example.test/two"}},
		}},
	}}
	packages, err := resolver.Packages("pkg", "x86_64", artifacts.Selection{Source: "lkg"}, distro)
	require.NoError(t, err)
	require.Len(t, packages, 2)
	assert.NotEmpty(t, packages[0].SHA256)
	assert.True(t, strings.HasSuffix(packages[0].Path, ".rpm"))

	distro.TestPackages["pkg"].Architectures["x86_64"][1].URI = "https://example.test/fail"
	packages, err = resolver.Packages("pkg", "x86_64", artifacts.Selection{Source: "lkg"}, distro)
	require.ErrorIs(t, err, nativeErr)
	assert.Nil(t, packages)
}
