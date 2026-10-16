// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build e2e && linux

package scenario_tests

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/utils/buildtestenv"
	"github.com/microsoft/azure-linux-dev-tools/scenario/internal/cmdtest"
	"github.com/microsoft/azure-linux-dev-tools/scenario/internal/containertest"
	"github.com/stretchr/testify/require"
)

const (
	azureLinuxRepositoryURL = "https://github.com/microsoft/azurelinux.git"
	azureLinuxImageBranch   = "4.0"
	azureLinuxImage         = "1p-vm-base-gen2"
	imageBuildTimeout       = 15 * time.Minute
)

type vmBaseFixture struct {
	imagePath string
	revision  string
	sha256    string
}

func TestAzureLinuxImageBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long test")
	}

	fixture := buildAzureLinuxImage(t, azureLinuxImage, "")
	t.Logf("Built Azure Linux %s revision %s with SHA-256 %s", azureLinuxImage, fixture.revision, fixture.sha256)
}

func buildAzureLinuxVMBase(t *testing.T) vmBaseFixture {
	t.Helper()

	revisionBytes, err := os.ReadFile(filepath.Join("testdata", "image-build", "azurelinux-revision"))
	require.NoError(t, err)
	revision := strings.TrimSpace(string(revisionBytes))
	require.Len(t, revision, 40, "Azure Linux revision must be a full commit SHA")

	return buildAzureLinuxImage(t, "vm-base", revision)
}

func buildAzureLinuxImage(t *testing.T, name, pinnedRevision string) vmBaseFixture {
	t.Helper()
	if os.Getenv(buildtestenv.TestingDisableNetworkTestsEnvVar) != "" {
		t.Skip("skipping image build because network tests are disabled")
	}
	artifactDirectory := t.TempDir()
	start := time.Now()

	test := cmdtest.NewScenarioTest().
		WithCustomCmd("build-image").
		WithArgs("x86_64", name, azureLinuxRepositoryURL, azureLinuxImageBranch, pinnedRevision).
		WithTimeout(imageBuildTimeout).
		InContainer().
		WithDockerfile("Dockerfile.image-builder").
		WithPrivilege().
		WithNetwork().
		WithExtraMounts([]containertest.ContainerMount{
			containertest.NewContainerMount(artifactDirectory, "/artifacts", nil),
		})

	results, err := test.Run(t)
	require.NoError(t, err)
	t.Logf("Image build standard output:\n%s", results.Stdout)
	t.Logf("Image build standard error:\n%s", results.Stderr)
	require.Zero(t, results.ExitCode)

	revisionBytes, err := os.ReadFile(filepath.Join(artifactDirectory, "source-revision"))
	require.NoError(t, err)
	revision := strings.TrimSpace(string(revisionBytes))
	require.Len(t, revision, 40)
	if pinnedRevision != "" {
		require.Equal(t, pinnedRevision, revision)
	}
	imagePath := filepath.Join(artifactDirectory, name+".qcow2")
	image, err := os.Open(imagePath)
	require.NoError(t, err)
	defer image.Close()

	hash := sha256.New()
	_, err = io.Copy(hash, image)
	require.NoError(t, err)

	sha := hex.EncodeToString(hash.Sum(nil))
	info, err := image.Stat()
	require.NoError(t, err)
	require.Positive(t, info.Size())

	t.Logf(
		"Built Azure Linux image %s revision %s in %s: size=%d sha256=%s",
		name, revision,
		time.Since(start),
		info.Size(),
		sha,
	)

	return vmBaseFixture{
		imagePath: imagePath,
		revision:  revision,
		sha256:    sha,
	}
}

func runHostCommand(t *testing.T, directory string, program string, args ...string) {
	t.Helper()

	command := exec.CommandContext(t.Context(), program, args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoError(t, err, "run %s %s:\n%s", program, strings.Join(args, " "), output)
}
