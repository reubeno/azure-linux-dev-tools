// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build e2e && linux

package scenario_tests

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/utils/buildtestenv"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/scenario/internal/cmdtest"
	"github.com/microsoft/azure-linux-dev-tools/scenario/internal/containertest"
	"github.com/stretchr/testify/require"
)

func TestNativeFrameworkContracts(t *testing.T) {
	if testing.Short() {
		t.Skip("native framework contracts require real tools and guests")
	}
	require.Empty(t, os.Getenv(buildtestenv.TestingDisableNetworkTestsEnvVar), "native contracts require network access")
	t.Run("local", func(t *testing.T) { runNativeFrameworkFamily(t, "local", "", false) })
	t.Run("virtual", func(t *testing.T) {
		inputs, external, err := loadTMTVirtualInputs(os.Getenv)
		require.NoError(t, err)
		if !external {
			fixture := buildAzureLinuxVMBase(t)
			inputs.imagePath, inputs.imageRevision = fixture.imagePath, fixture.revision
		}
		t.Run("tmt", func(t *testing.T) { runTMTVirtualProbe(t, inputs, external) })
		t.Run("lisa", func(t *testing.T) {
			image := os.Getenv("AZL_LISA_IMAGE")
			if image == "" {
				image = qemuCompatibleFixture(t, inputs.imagePath)
			}
			require.True(t, filepath.IsAbs(image))
			require.FileExists(t, image)
			before := fixtureDigest(t, image)
			runNativeFrameworkFamily(t, "lisa", image, true)
			require.Equal(t, before, fixtureDigest(t, image), "the original supporting image must not change")
		})
	})
}

func runNativeFrameworkFamily(t *testing.T, family, image string, systemd bool) {
	t.Helper()
	base, err := filepath.Abs(filepath.Join("..", "out", "framework-e2e"))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(base, fileperms.PrivateDir))
	output, err := os.MkdirTemp(base, family+"-")
	require.NoError(t, err)
	runtime := t.TempDir()
	t.Logf("Native %s contract artifacts: %s", family, output)
	mounts := []containertest.ContainerMount{containertest.NewContainerMount(runtime, "/artifacts", nil)}
	if image != "" {
		mounts = append(mounts, containertest.NewContainerMount(image, "/inputs/vm-base.qcow2",
			[]containertest.ContainerMountFlag{containertest.ContainerMountFlagReadOnly}))
	}
	test := cmdtest.NewScenarioTest().
		WithCustomCmd("python3").
		WithArgs("/workdir/native-framework-contracts.py", family).
		WithEnv(map[string]string{"AZLDEV_NATIVE_WORK": "/artifacts"}).
		WithTimeout(25 * time.Minute).
		InContainer().
		WithDockerfile("Dockerfile.tmt").
		WithNetwork().
		WithPrivilege().
		WithExtraMounts(mounts)
	if systemd {
		test = test.WithSystemd()
	} else {
		test = test.WithInit()
	}
	test.AddDirRecursive(t, "fixture", filepath.Join("testdata", "frameworks"))
	driver, err := filepath.Abs(filepath.Join("docker", "native-framework-contracts.py"))
	require.NoError(t, err)
	test.AddSingleFile("native-framework-contracts.py", driver)
	results, runErr := test.Run(t)
	collectNativeReports(t, runtime, output)
	require.NoError(t, os.WriteFile(filepath.Join(output, "container.stdout.log"), []byte(results.Stdout), fileperms.PrivateFile))
	require.NoError(t, os.WriteFile(filepath.Join(output, "container.stderr.log"), []byte(results.Stderr), fileperms.PrivateFile))
	require.NoError(t, runErr, "native %s contracts:\n%s\n%s", family, results.Stdout, results.Stderr)
	require.Zero(t, results.ExitCode, "native %s contracts:\n%s\n%s", family, results.Stdout, results.Stderr)
	t.Logf("Native %s results:\n%s", family, results.Stdout)
}

func collectNativeReports(t *testing.T, runtime, output string) {
	t.Helper()
	for _, pattern := range []string{
		"*.log", "*.xml", "*.txt", "timings.json", "artifacts/*",
		"runs/*/tests/*/results.json", "runs/*/tests/*/*/inputs.json",
		"runs/*/tests/*/*/junit.xml", "runs/*/tests/*/*/python-environment.txt",
		"runs/*/tests/*/*/tmt/tmt-output.log",
		"runs/*/tests/*/*/*/*/*.log", "runs/*/tests/*/*/*/*/*/*.log",
		"runs/*/tests/*/*/*/*/environments/*/*/*.log",
		"runs/*/tests/*/*/*/*/tests/*/*/*/*.log",
	} {
		paths, err := filepath.Glob(filepath.Join(runtime, pattern))
		require.NoError(t, err)
		for _, path := range paths {
			relative, err := filepath.Rel(runtime, path)
			require.NoError(t, err)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			dest := filepath.Join(output, relative)
			require.NoError(t, os.MkdirAll(filepath.Dir(dest), fileperms.PrivateDir))
			require.NoError(t, os.WriteFile(dest, data, fileperms.PrivateFile))
		}
	}
}

func qemuCompatibleFixture(t *testing.T, source string) string {
	t.Helper()
	root := t.TempDir()
	files, err := os.ReadDir(filepath.Join("testdata", "frameworks", "qemu-image"))
	require.NoError(t, err)
	for _, entry := range files {
		data, err := os.ReadFile(filepath.Join("testdata", "frameworks", "qemu-image", entry.Name()))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, entry.Name()), data, fileperms.PrivateFile))
	}
	outputDir := filepath.Join(root, "out")
	require.NoError(t, os.Mkdir(outputDir, fileperms.PrivateDir))
	output := filepath.Join(outputDir, "lisa.qcow2")
	binary := os.Getenv(buildtestenv.TestingAzldevBinPathEnvVar)
	require.NotEmpty(t, binary)
	before := fixtureDigest(t, source)
	runHostCommand(t, root, binary, "-C", root, "image", "customize", "--image-file", source,
		"--image-config", filepath.Join(root, "config.yaml"), "--output-image-format", "qcow2", "--output-path", output)
	require.Equal(t, before, fixtureDigest(t, source))
	return output
}

func fixtureDigest(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	require.NoError(t, err)
	return strings.ToLower(hex.EncodeToString(hash.Sum(nil)))
}
