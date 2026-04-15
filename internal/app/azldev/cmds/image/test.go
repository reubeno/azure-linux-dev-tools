// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package image

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
)

const (
	// testRunnerLisa is the LISA test framework identifier.
	testRunnerLisa = "lisa"

	// testImagePrefix is the prefix used for qcow2 images created during image testing.
	testImagePrefix = "azldevtest"
)

// ImageTestOptions holds the options for the 'image test' command.
type ImageTestOptions struct {
	// Name selects a test suite defined in the [tests] TOML section.
	Name string

	// ImagePath is the path to the image file to test.
	ImagePath string

	// ManifestPath is an optional path to a kiwi .packages manifest file.
	ManifestPath string

	// JUnitXMLPath is an optional path for writing JUnit XML output (pytest only).
	JUnitXMLPath string

	// PytestArgs are extra arguments passed through to pytest.
	PytestArgs []string
}

func testOnAppInit(_ *azldev.App, parentCmd *cobra.Command) {
	parentCmd.AddCommand(NewImageTestCmd())
}

// NewImageTestCmd constructs a [cobra.Command] for the 'image test' command.
func NewImageTestCmd() *cobra.Command {
	options := &ImageTestOptions{}

	cmd := &cobra.Command{
		Use:   "test",
		Short: "Run tests against an Azure Linux image",
		Long: `Run tests against an Azure Linux image using a test suite defined in the
project configuration.

Test suites are defined in the [tests] section of azldev.toml and referenced
by images via the 'tests' field. Each test suite specifies a type (pytest or
lisa) and framework-specific configuration.

For pytest tests, the test runner executes inside a mock chroot with
pre-installed dependencies. The image file and test directory are bind-mounted
into the chroot.

For LISA tests, the test runner executes on the host and boots the image in a
QEMU VM.`,
		Example: `  # Run a pytest-based test suite
  azldev image test --name smoke --image-path ./out/image.qcow2

  # Run with a kiwi manifest for package validation
  azldev image test --name smoke --image-path ./out/image.qcow2 --manifest ./out/image.packages

  # Run a LISA-based test suite
  azldev image test --name integration --image-path ./out/image.qcow2

  # Generate JUnit XML output (pytest only)
  azldev image test --name smoke --image-path ./out/image.qcow2 --junit-xml results.xml`,
		RunE: azldev.RunFunc(func(env *azldev.Env) (interface{}, error) {
			return nil, runImageTest(env, options)
		}),
	}

	cmd.Flags().StringVar(&options.Name, "name", "",
		"Name of the test suite (as defined in [tests] section of azldev.toml)")
	_ = cmd.MarkFlagRequired("name")

	cmd.Flags().StringVarP(&options.ImagePath, "image-path", "i", "",
		"Path to the disk image file to test")
	_ = cmd.MarkFlagRequired("image-path")
	_ = cmd.MarkFlagFilename("image-path")

	cmd.Flags().StringVar(&options.ManifestPath, "manifest", "",
		"Path to a kiwi .packages manifest file (optional, for pytest tests)")
	_ = cmd.MarkFlagFilename("manifest")

	cmd.Flags().StringVar(&options.JUnitXMLPath, "junit-xml", "",
		"Path for writing JUnit XML output (pytest only)")
	_ = cmd.MarkFlagFilename("junit-xml")

	return cmd
}

// runImageTest dispatches to the appropriate test runner based on the test suite's type.
func runImageTest(env *azldev.Env, options *ImageTestOptions) error {
	// Resolve the test suite from the project config.
	testConfig, err := resolveTestByName(env, options.Name)
	if err != nil {
		return err
	}

	// Validate that the image file exists.
	if err := validateFileExists(env.FS(), options.ImagePath); err != nil {
		return fmt.Errorf("--image-path:\n%w", err)
	}

	// If a manifest was provided, validate it exists.
	if options.ManifestPath != "" {
		if err := validateFileExists(env.FS(), options.ManifestPath); err != nil {
			return fmt.Errorf("--manifest:\n%w", err)
		}
	}

	switch testConfig.Type {
	case projectconfig.TestTypePytest:
		return runPytestSuite(env, testConfig, options)

	case projectconfig.TestTypeLisa:
		return runLisaSuite(env, testConfig, options)

	default:
		return fmt.Errorf("unsupported test type %#q for test %#q", testConfig.Type, testConfig.Name)
	}
}

// resolveTestByName looks up a test suite by name in the project configuration.
func resolveTestByName(env *azldev.Env, testName string) (*projectconfig.TestConfig, error) {
	cfg := env.Config()
	if cfg == nil {
		return nil, errors.New("no project configuration loaded")
	}

	if testName == "" {
		return nil, errors.New("test name is required")
	}

	if testConfig, ok := cfg.Tests[testName]; ok {
		return &testConfig, nil
	}

	// Not found; list available tests in the error message.
	availableTests := lo.Keys(cfg.Tests)
	sort.Strings(availableTests)

	if len(availableTests) == 0 {
		return nil, fmt.Errorf("test %#q not found; no tests defined in project configuration", testName)
	}

	return nil, fmt.Errorf(
		"test %#q not found; available tests: %s",
		testName, strings.Join(availableTests, ", "),
	)
}

// runLisaSuite runs a LISA-based test suite.
func runLisaSuite(env *azldev.Env, testConfig *projectconfig.TestConfig, options *ImageTestOptions) error {
	// Validate LISA-specific prerequisites.
	if err := checkLisaInstalled(env); err != nil {
		return err
	}

	if err := validateFileExists(env.FS(), testConfig.AdminPrivateKeyPath); err != nil {
		return fmt.Errorf("admin-private-key-path for test %#q:\n%w", testConfig.Name, err)
	}

	// Resolve the image to qcow2 format (LISA requires it).
	qcow2Path, err := ResolveQcow2Image(env, options.ImagePath)
	if err != nil {
		return err
	}

	return runLisa(env, testConfig.RunbookPath, qcow2Path, testConfig.AdminPrivateKeyPath)
}

// CheckTestRunner returns an error if the test runner is not supported.
func CheckTestRunner(runner string) error {
	if !strings.EqualFold(runner, testRunnerLisa) {
		return fmt.Errorf("test runner %#q is not supported; only %#q is supported at this time", runner, testRunnerLisa)
	}

	return nil
}

// checkLisaInstalled verifies that the lisa executable is available on the host.
func checkLisaInstalled(env *azldev.Env) error {
	if !env.CommandInSearchPath(testRunnerLisa) {
		return errors.New("'lisa' is not installed or not found in PATH; " +
			"please install LISA before running image tests")
	}

	return nil
}

// ResolveQcow2Image inspects the image at imagePath and returns a path to a qcow2 image.
// If the image is already qcow2 it is returned as-is. If it is vhd or vhdfixed it is
// converted to qcow2 in a temporary directory. Any other format is an error.
func ResolveQcow2Image(env *azldev.Env, imagePath string) (string, error) {
	format, err := InferImageFormat(imagePath)
	if err != nil {
		return "", err
	}

	switch format {
	case string(ImageFormatQcow2):
		slog.Info("Image is already in qcow2 format, using as-is", slog.String("path", imagePath))

		return imagePath, nil

	case string(ImageFormatVhd):
		return convertToQcow2(env, imagePath)

	default:
		return "", fmt.Errorf(
			"image format %#q is not supported for testing; supported formats: qcow2, vhd, vhdfixed",
			format,
		)
	}
}

// convertToQcow2 converts a vhd/vhdfixed disk image to qcow2 format using qemu-img and
// returns the path to the converted file. The converted image is written alongside the
// source file and is the caller's responsibility to clean up (or accept it as a leftover).
func convertToQcow2(env *azldev.Env, srcPath string) (string, error) {
	if !env.CommandInSearchPath("qemu-img") {
		return "", errors.New("'qemu-img' is not installed or not found in PATH; " +
			"it is required to convert vhd/vhdfixed images to qcow2")
	}

	baseName := strings.TrimSuffix(filepath.Base(srcPath), filepath.Ext(srcPath))
	destFileName := baseName + ".qcow2"

	// Write the converted image alongside the source file so the path is predictable.
	destPath := filepath.Join(filepath.Dir(srcPath), testImagePrefix+"-"+destFileName)

	slog.Info("Converting image to qcow2",
		slog.String("src", srcPath),
		slog.String("dest", destPath),
	)

	if env.DryRun() {
		slog.Info("Dry-run: would convert image to qcow2",
			slog.String("src", srcPath),
			slog.String("dest", destPath),
		)

		return destPath, nil
	}

	convertCmd := exec.CommandContext(
		env, "qemu-img", "convert", "-O", "qcow2", srcPath, destPath,
	)
	convertCmd.Stdout = os.Stdout
	convertCmd.Stderr = os.Stderr

	cmd, err := env.Command(convertCmd)
	if err != nil {
		return "", fmt.Errorf("failed to create qemu-img command:\n%w", err)
	}

	if err = cmd.Run(env); err != nil {
		return "", fmt.Errorf("failed to convert image %#q to qcow2:\n%w", srcPath, err)
	}

	slog.Info("Conversion complete", slog.String("dest", destPath))

	return destPath, nil
}

// validateFileExists returns an error if the path does not point to an existing regular file.
func validateFileExists(fs opctx.FS, path string) error {
	isDir, err := fileutils.DirExists(fs, path)
	if err != nil {
		return fmt.Errorf("cannot access %#q:\n%w", path, err)
	}

	if isDir {
		return fmt.Errorf("%#q is a directory, expected a file", path)
	}

	exists, err := fileutils.Exists(fs, path)
	if err != nil {
		return fmt.Errorf("cannot access %#q:\n%w", path, err)
	}

	if !exists {
		return fmt.Errorf("file not found: %#q", path)
	}

	return nil
}

// runLisa executes `lisa -r <runbookPath> -v "qcow2:<imagePath>"` and streams its
// stdout and stderr directly to the terminal.
func runLisa(env *azldev.Env, runbookPath, qcow2ImagePath, adminPrivateKeyPath string) error {
	slog.Info("Running LISA tests",
		slog.String("runbook", runbookPath),
		slog.String("image", qcow2ImagePath),
	)

	args := []string{
		"-r", runbookPath,
		"-v", "qcow2:" + qcow2ImagePath,
		"-v", "admin_private_key_file:" + adminPrivateKeyPath,
	}

	lisaCmd := exec.CommandContext(
		env,
		testRunnerLisa,
		args...,
	)
	lisaCmd.Stdout = os.Stdout
	lisaCmd.Stderr = os.Stderr

	cmd, err := env.Command(lisaCmd)
	if err != nil {
		return fmt.Errorf("failed to create lisa command:\n%w", err)
	}

	if err = cmd.Run(env); err != nil {
		return fmt.Errorf("lisa test run failed:\n%w", err)
	}

	return nil
}
