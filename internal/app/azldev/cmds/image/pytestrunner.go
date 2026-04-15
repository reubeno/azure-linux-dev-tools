// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package image

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/buildenvfactory"
	"github.com/microsoft/azure-linux-dev-tools/internal/buildenv"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/rpm/mock"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	pytestfixtures "github.com/microsoft/azure-linux-dev-tools/python"
)

const (
	// Paths inside the mock chroot where artifacts are bind-mounted.
	mockTestDir     = "/azldev-tests"
	mockImagePath   = "/azldev-image"
	mockFixturesDir = "/azldev-fixtures"
	mockConfigDir   = "/azldev-config"
	mockManifestDir = "/azldev-manifest"

	// Base packages required for running pytest in a mock chroot.
	pytestPackage = "python3-pytest"
)

// runPytestSuite runs a pytest-based test suite inside a mock chroot.
func runPytestSuite(
	env *azldev.Env, testConfig *projectconfig.TestConfig, options *ImageTestOptions,
) error {
	slog.Info("Running pytest test suite",
		slog.String("name", testConfig.Name),
		slog.String("test-dir", testConfig.TestDir),
		slog.String("image-path", options.ImagePath),
	)

	// Validate that the test directory exists.
	testDirExists, err := fileutils.DirExists(env.FS(), testConfig.TestDir)
	if err != nil {
		return fmt.Errorf("cannot access test directory %#q:\n%w", testConfig.TestDir, err)
	}

	if !testDirExists {
		return fmt.Errorf("test directory not found: %#q", testConfig.TestDir)
	}

	// Set up the mock runner.
	runner, err := makePytestMockRunner(env)
	if err != nil {
		return err
	}

	// Install required packages in the mock chroot.
	packagesToInstall := []string{pytestPackage}
	packagesToInstall = append(packagesToInstall, testConfig.MockPackages...)

	slog.Info("Installing packages in mock chroot", slog.Any("packages", packagesToInstall))

	if err := runner.InstallPackages(env, packagesToInstall); err != nil {
		return fmt.Errorf("failed to install packages in mock chroot:\n%w", err)
	}

	// Configure bind mounts and build the pytest arguments.
	pytestArgs, err := preparePytestEnvironment(env, runner, testConfig, options)
	if err != nil {
		return err
	}

	slog.Info("Running pytest in mock chroot", slog.Any("args", pytestArgs))

	cmd, err := runner.CmdInChroot(env, pytestArgs, false /*interactive*/)
	if err != nil {
		return fmt.Errorf("failed to create pytest command:\n%w", err)
	}

	cmd.SetStdout(os.Stdout)
	cmd.SetStderr(os.Stderr)

	if err := cmd.Run(env); err != nil {
		return fmt.Errorf("pytest run failed:\n%w", err)
	}

	return nil
}

// preparePytestEnvironment sets up bind mounts for the mock chroot and returns the
// constructed pytest command-line arguments.
func preparePytestEnvironment(
	env *azldev.Env, runner *mock.Runner,
	testConfig *projectconfig.TestConfig, options *ImageTestOptions,
) ([]string, error) {
	// Bind-mount the test directory.
	runner.AddBindMount(testConfig.TestDir, mockTestDir)

	// Bind-mount the image file. Resolve symlinks first because kiwi output
	// directories often contain symlinks back to the work directory, and the
	// symlink targets are not visible inside the mock chroot.
	absImagePath, err := filepath.Abs(options.ImagePath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve absolute path for image %#q:\n%w", options.ImagePath, err)
	}

	realImagePath, err := filepath.EvalSymlinks(absImagePath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve symlinks for image %#q:\n%w", absImagePath, err)
	}

	imageFileName := filepath.Base(realImagePath)
	mockImageFilePath := filepath.Join(mockImagePath, imageFileName)
	runner.AddBindMount(filepath.Dir(realImagePath), mockImagePath)

	// Extract and bind-mount the Python fixtures.
	fixturesDir, err := extractPytestFixtures(env.FS(), env.WorkDir())
	if err != nil {
		return nil, fmt.Errorf("failed to extract pytest fixtures:\n%w", err)
	}

	runner.AddBindMount(fixturesDir, mockFixturesDir)

	// Serialize image config to JSON and bind-mount it.
	configJSONPath := ""

	imageConfig, err := findImageForTest(env, testConfig.Name)
	if err != nil {
		slog.Warn("Could not resolve image config for test context; proceeding without it",
			slog.String("test", testConfig.Name),
			slog.Any("error", err),
		)
	}

	if imageConfig != nil {
		configJSONPath, err = mountImageConfig(env, runner, imageConfig)
		if err != nil {
			return nil, err
		}
	}

	// If a manifest was provided, resolve its path inside the mock chroot.
	// If the manifest is in the same directory as the image, reuse the existing
	// /azldev-image mount rather than creating a duplicate bind mount for the
	// same host directory (which mock may silently ignore).
	mockManifestFilePath := ""

	if options.ManifestPath != "" {
		absManifestPath, err := filepath.Abs(options.ManifestPath)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve absolute path for manifest %#q:\n%w",
				options.ManifestPath, err)
		}

		realManifestPath, err := filepath.EvalSymlinks(absManifestPath)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve symlinks for manifest %#q:\n%w",
				absManifestPath, err)
		}

		manifestFileName := filepath.Base(realManifestPath)
		manifestDir := filepath.Dir(realManifestPath)
		imageDir := filepath.Dir(realImagePath)

		if manifestDir == imageDir {
			// Same directory — reference via the already-mounted /azldev-image.
			mockManifestFilePath = filepath.Join(mockImagePath, manifestFileName)
		} else {
			mockManifestFilePath = filepath.Join(mockManifestDir, manifestFileName)
			runner.AddBindMount(manifestDir, mockManifestDir)
		}
	}

	// Build the pytest command arguments.
	pytestArgs := BuildPytestArgs(
		mockTestDir,
		mockImageFilePath,
		configJSONPath,
		mockManifestFilePath,
		options.JUnitXMLPath,
		mockFixturesDir,
	)

	return pytestArgs, nil
}

// mountImageConfig serializes the image config to JSON in a temp directory and
// bind-mounts that directory into the mock chroot.
func mountImageConfig(
	env *azldev.Env, runner *mock.Runner, imageConfig *projectconfig.ImageConfig,
) (string, error) {
	configDir, err := fileutils.MkdirTemp(env.FS(), env.WorkDir(), "azldev-test-config-")
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir for config:\n%w", err)
	}

	configJSONPath, err := SerializeImageConfigToJSON(env.FS(), imageConfig, configDir)
	if err != nil {
		return "", fmt.Errorf("failed to serialize image config:\n%w", err)
	}

	runner.AddBindMount(configDir, mockConfigDir)

	return configJSONPath, nil
}

// BuildPytestArgs constructs the command-line arguments for running pytest inside the mock chroot.
func BuildPytestArgs(
	testDir, imagePath, configJSONPath, manifestPath, junitXMLPath, fixturesDir string,
) []string {
	// Set PYTHONPATH to include the fixtures directory so that azldev_check can be imported.
	// Use env command to set the environment variable before running pytest.
	args := []string{
		"env",
		"PYTHONPATH=" + fixturesDir,
		"python3", "-m", "pytest",
		testDir,
		"-v",
		// Register the azldev_check conftest as a plugin.
		"-p", "azldev_check.conftest",
		"--azldev-image", imagePath,
	}

	if configJSONPath != "" {
		// Map the config path to its location inside the mock chroot.
		mockConfigJSONPath := filepath.Join(mockConfigDir, filepath.Base(configJSONPath))
		args = append(args, "--azldev-config", mockConfigJSONPath)
	}

	if manifestPath != "" {
		args = append(args, "--azldev-manifest", manifestPath)
	}

	if junitXMLPath != "" {
		args = append(args, "--junit-xml", junitXMLPath)
	}

	return args
}

// findImageForTest finds an image config that references the given test name.
func findImageForTest(env *azldev.Env, testName string) (*projectconfig.ImageConfig, error) {
	cfg := env.Config()
	if cfg == nil {
		return nil, errors.New("no project configuration loaded")
	}

	for _, image := range cfg.Images {
		for _, t := range image.Tests {
			if t == testName {
				return &image, nil
			}
		}
	}

	return nil, fmt.Errorf("no image references test %#q", testName)
}

// makePytestMockRunner creates a mock runner suitable for running pytest.
func makePytestMockRunner(env *azldev.Env) (*mock.Runner, error) {
	factory, err := buildenvfactory.NewMockRootFactoryForEnv(env)
	if err != nil {
		return nil, fmt.Errorf("failed to create mock root factory:\n%w", err)
	}

	root, err := factory.CreateMockRoot(buildenv.CreateOptions{
		Name:        "azldev-pytest",
		Description: "Mock environment for running pytest image checks",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create mock root:\n%w", err)
	}

	return root.GetRunner(), nil
}

// extractPytestFixtures extracts the embedded Python fixture files to a temporary directory
// under workDir. Returns the path to the directory containing the azldev_check package.
func extractPytestFixtures(fs opctx.FS, workDir string) (string, error) {
	fixturesDir, err := fileutils.MkdirTemp(fs, workDir, "azldev-pytest-fixtures-")
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir for pytest fixtures:\n%w", err)
	}

	if err := pytestfixtures.ExtractTo(fs, fixturesDir); err != nil {
		return "", fmt.Errorf("failed to extract pytest fixtures:\n%w", err)
	}

	return fixturesDir, nil
}
