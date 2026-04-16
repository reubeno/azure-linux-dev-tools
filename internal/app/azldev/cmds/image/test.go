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
	// ImageName is the name of the image (positional argument), used to look up its
	// test suites and optionally resolve the image artifact path.
	ImageName string

	// TestSuites optionally selects specific test suites to run. When empty, all test
	// suites associated with the image are run.
	TestSuites []string

	// ImagePath is an optional explicit path to the image file. When empty, the image
	// artifact is resolved from the image name in the output directory.
	ImagePath string

	// JUnitXMLPath is an optional path for writing JUnit XML output.
	JUnitXMLPath string
}

func testOnAppInit(_ *azldev.App, parentCmd *cobra.Command) {
	parentCmd.AddCommand(NewImageTestCmd())
}

// NewImageTestCmd constructs a [cobra.Command] for the 'image test' command.
func NewImageTestCmd() *cobra.Command {
	options := &ImageTestOptions{}

	cmd := &cobra.Command{
		Use:   "test IMAGE_NAME",
		Short: "Run tests against an Azure Linux image",
		Long: `Run tests against an Azure Linux image using test suites defined in the
project configuration.

Test suites are defined in the [test-suites] section of azldev.toml and referenced
by images via the [images.NAME.tests] subtable. Each test suite specifies a type
(pytest or lisa) and framework-specific configuration in a matching subtable.

By default, all test suites associated with the named image are run. Use
--test-suite to select specific suites (may be repeated).

The image artifact can be specified explicitly with --image-path, or resolved
automatically from the image name in the output directory.

For pytest tests, azldev creates a Python virtual environment, installs
dependencies from pyproject.toml in the working directory, and runs pytest
with the configured test paths and extra arguments. Use {image-path} in
extra-args to insert the image path. Glob patterns (including **) in
test-paths are expanded automatically.

For LISA tests, the test runner executes on the host and boots the image in a
QEMU VM.`,
		Example: `  # Run all test suites for an image (artifact auto-resolved from output dir)
  azldev image test vm-base

  # Run all test suites with an explicit image path
  azldev image test vm-base --image-path ./out/images/vm-base/image.raw

  # Run a specific test suite
  azldev image test vm-base --test-suite common-vm-checks

  # Run multiple specific test suites
  azldev image test vm-base --test-suite common-vm-checks --test-suite vm-base-checks

  # Generate JUnit XML output
  azldev image test vm-base --junit-xml results.xml`,
		Args: cobra.ExactArgs(1),
		RunE: azldev.RunFuncWithExtraArgs(func(env *azldev.Env, args []string) (interface{}, error) {
			options.ImageName = args[0]

			return nil, runImageTest(env, options)
		}),
		ValidArgsFunction: generateImageNameCompletions,
	}

	cmd.Flags().StringSliceVar(&options.TestSuites, "test-suite", nil,
		"Name of a test suite to run (may be repeated; defaults to all suites for the image)")

	cmd.Flags().StringVarP(&options.ImagePath, "image-path", "i", "",
		"Path to the disk image file (resolved from image name if not specified)")
	_ = cmd.MarkFlagFilename("image-path")

	cmd.Flags().StringVar(&options.JUnitXMLPath, "junit-xml", "",
		"Path for writing JUnit XML output")
	_ = cmd.MarkFlagFilename("junit-xml")

	return cmd
}

// runImageTest resolves which test suites to run and dispatches each one.
func runImageTest(env *azldev.Env, options *ImageTestOptions) error {
	cfg := env.Config()
	if cfg == nil {
		return errors.New("no project configuration loaded")
	}

	// Resolve the image config from the positional argument.
	imageConfig, err := ResolveImageByName(env, options.ImageName)
	if err != nil {
		return err
	}

	// Resolve image path: explicit --image-path takes precedence, otherwise resolve
	// from the image name in the output directory.
	imagePath := options.ImagePath
	if imagePath == "" {
		var resolveErr error

		imagePath, _, resolveErr = findImageArtifact(env, options.ImageName, "", AllImageFormats())
		if resolveErr != nil {
			return resolveErr
		}

		slog.Info("Resolved image artifact",
			slog.String("image", options.ImageName),
			slog.String("path", imagePath),
		)
	}

	// Validate that the image file exists.
	if err := validateFileExists(env.FS(), imagePath); err != nil {
		return fmt.Errorf("image path:\n%w", err)
	}

	options.ImagePath = imagePath

	// Determine which test suites to run.
	suiteNames := resolveTestSuiteNames(imageConfig, options.TestSuites)

	if len(suiteNames) == 0 {
		slog.Warn("No test suites to run for image", slog.String("image", options.ImageName))

		return nil
	}

	// Resolve and run each test suite, continuing past failures so all suites get a chance
	// to run. Config/resolution errors abort immediately since they indicate a broken setup.
	var testFailures []string

	for _, suiteName := range suiteNames {
		testConfig, err := resolveTestSuiteByName(cfg, suiteName)
		if err != nil {
			return err
		}

		if err := runTestSuite(env, testConfig, options); err != nil {
			slog.Error("Test suite failed",
				slog.String("suite", suiteName),
				slog.Any("error", err),
			)

			testFailures = append(testFailures, suiteName)
		}
	}

	if len(testFailures) > 0 {
		return fmt.Errorf("%d of %d test suite(s) failed: %s",
			len(testFailures), len(suiteNames), strings.Join(testFailures, ", "))
	}

	return nil
}

// resolveTestSuiteNames determines which test suites to run. If explicit names are
// provided, they are used as-is. Otherwise, all test suites associated with the image
// are returned.
func resolveTestSuiteNames(
	imageConfig *projectconfig.ImageConfig, explicitSuites []string,
) []string {
	if len(explicitSuites) > 0 {
		return explicitSuites
	}

	return imageConfig.TestNames()
}

// resolveTestSuiteByName looks up a test suite by name in the project configuration.
func resolveTestSuiteByName(
	cfg *projectconfig.ProjectConfig, suiteName string,
) (*projectconfig.TestConfig, error) {
	testConfig, ok := cfg.TestSuites[suiteName]
	if !ok {
		availableSuites := lo.Keys(cfg.TestSuites)
		sort.Strings(availableSuites)

		if len(availableSuites) == 0 {
			return nil, fmt.Errorf(
				"test suite %#q not found; no test suites defined in project configuration", suiteName)
		}

		return nil, fmt.Errorf(
			"test suite %#q not found; available test suites: %s",
			suiteName, strings.Join(availableSuites, ", "),
		)
	}

	return &testConfig, nil
}

// runTestSuite dispatches a single test suite to the appropriate runner.
func runTestSuite(
	env *azldev.Env, testConfig *projectconfig.TestConfig, options *ImageTestOptions,
) error {
	switch testConfig.Type {
	case projectconfig.TestTypePytest:
		return RunPytestSuite(env, testConfig, options)

	case projectconfig.TestTypeLisa:
		return runLisaSuite(env, testConfig, options)

	default:
		return fmt.Errorf("unsupported test type %#q for test suite %#q", testConfig.Type, testConfig.Name)
	}
}

// runLisaSuite runs a LISA-based test suite.
func runLisaSuite(env *azldev.Env, testConfig *projectconfig.TestConfig, options *ImageTestOptions) error {
	lisaConfig := testConfig.Lisa
	if lisaConfig == nil {
		return fmt.Errorf("test %#q is missing lisa configuration", testConfig.Name)
	}

	// Validate LISA-specific prerequisites.
	if err := checkLisaInstalled(env); err != nil {
		return err
	}

	if err := validateFileExists(env.FS(), lisaConfig.AdminPrivateKeyPath); err != nil {
		return fmt.Errorf("admin-private-key-path for test %#q:\n%w", testConfig.Name, err)
	}

	// Resolve the image to qcow2 format (LISA requires it).
	qcow2Path, err := ResolveQcow2Image(env, options.ImagePath)
	if err != nil {
		return err
	}

	return runLisa(env, lisaConfig.RunbookPath, qcow2Path, lisaConfig.AdminPrivateKeyPath)
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
