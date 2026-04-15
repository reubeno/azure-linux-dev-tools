// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package image

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/prereqs"
)

const (
	// pythonProgram is the Python interpreter used to create venvs and run pytest.
	pythonProgram = "python3"

	// venvDirName is the name of the venv directory created under the azldev work dir.
	venvDirName = "pytest-venv"

	// imagePlaceholder is the placeholder token in pytest extra-args that gets replaced with
	// the actual image path at runtime.
	imagePlaceholder = "{image-path}"
)

// RunPytestSuite runs a pytest-based test suite natively using a Python venv.
func RunPytestSuite(
	env *azldev.Env, testConfig *projectconfig.TestConfig, options *ImageTestOptions,
) error {
	pytestConfig := testConfig.Pytest
	if pytestConfig == nil {
		return fmt.Errorf("test %#q is missing pytest configuration", testConfig.Name)
	}

	slog.Info("Running pytest test suite",
		slog.String("name", testConfig.Name),
		slog.String("working-dir", pytestConfig.WorkingDir),
		slog.String("image-path", options.ImagePath),
	)

	// Validate that the working directory exists.
	if pytestConfig.WorkingDir != "" {
		workingDirExists, err := fileutils.DirExists(env.FS(), pytestConfig.WorkingDir)
		if err != nil {
			return fmt.Errorf("cannot access working directory %#q:\n%w", pytestConfig.WorkingDir, err)
		}

		if !workingDirExists {
			return fmt.Errorf("working directory not found: %#q", pytestConfig.WorkingDir)
		}
	}

	// Ensure python3 is available.
	if err := prereqs.RequireExecutable(env, pythonProgram, nil); err != nil {
		return fmt.Errorf("python3 is required to run pytest tests:\n%w", err)
	}

	// Set up or reuse the venv.
	venvDir, err := ensurePytestVenv(env, testConfig.Name, pytestConfig.WorkingDir)
	if err != nil {
		return err
	}

	// Build the pytest command: expand test paths, substitute placeholders in extra args.
	pytestArgs := BuildNativePytestArgs(pytestConfig, options)

	slog.Info("Running pytest", slog.Any("args", pytestArgs))

	venvPython := filepath.Join(venvDir, "bin", pythonProgram)

	cmdArgs := append([]string{"-m", "pytest"}, pytestArgs...)

	pytestCmd := exec.CommandContext(env, venvPython, cmdArgs...)
	pytestCmd.Dir = pytestConfig.WorkingDir
	pytestCmd.Stdout = os.Stdout
	pytestCmd.Stderr = os.Stderr

	cmd, err := env.Command(pytestCmd)
	if err != nil {
		return fmt.Errorf("failed to create pytest command:\n%w", err)
	}

	if err := cmd.Run(env); err != nil {
		return fmt.Errorf("pytest run failed:\n%w", err)
	}

	return nil
}

// ensurePytestVenv creates or reuses a Python venv for the given test suite and installs
// dependencies from the working directory's pyproject.toml (if present). The venv is
// created under the project's work directory.
func ensurePytestVenv(env *azldev.Env, testName string, workingDir string) (string, error) {
	venvDir := filepath.Join(env.WorkDir(), venvDirName, testName)

	venvPython := filepath.Join(venvDir, "bin", pythonProgram)

	venvExists, err := fileutils.Exists(env.FS(), venvPython)
	if err != nil {
		return "", fmt.Errorf("cannot check venv at %#q:\n%w", venvDir, err)
	}

	if !venvExists {
		if err := createPythonVenv(env, venvDir); err != nil {
			return "", err
		}
	} else {
		slog.Info("Reusing existing Python venv", slog.String("path", venvDir))
	}

	// Always refresh dependencies if a pyproject.toml exists in the working directory.
	if err := installPytestDependencies(env, venvPython, workingDir); err != nil {
		return "", err
	}

	return venvDir, nil
}

// createPythonVenv creates a new Python virtual environment at venvDir.
func createPythonVenv(env *azldev.Env, venvDir string) error {
	slog.Info("Creating Python venv", slog.String("path", venvDir))

	venvCmd := exec.CommandContext(env, pythonProgram, "-m", "venv", venvDir)
	venvCmd.Stdout = os.Stdout
	venvCmd.Stderr = os.Stderr

	cmd, err := env.Command(venvCmd)
	if err != nil {
		return fmt.Errorf("failed to create venv command:\n%w", err)
	}

	if err := cmd.Run(env); err != nil {
		return fmt.Errorf("failed to create Python venv at %#q:\n%w", venvDir, err)
	}

	return nil
}

// installPytestDependencies installs dependencies from pyproject.toml (if present) into the
// given venv Python interpreter.
func installPytestDependencies(env *azldev.Env, venvPython string, workingDir string) error {
	if workingDir == "" {
		return nil
	}

	pyprojectPath := filepath.Join(workingDir, "pyproject.toml")

	pyprojectExists, err := fileutils.Exists(env.FS(), pyprojectPath)
	if err != nil {
		return fmt.Errorf("cannot check for pyproject.toml at %#q:\n%w", pyprojectPath, err)
	}

	if !pyprojectExists {
		return nil
	}

	slog.Info("Installing dependencies from pyproject.toml",
		slog.String("pyproject", pyprojectPath),
	)

	pipCmd := exec.CommandContext(
		env, venvPython, "-m", "pip", "install", "--quiet", "-e", workingDir,
	)
	pipCmd.Stdout = os.Stdout
	pipCmd.Stderr = os.Stderr

	cmd, err := env.Command(pipCmd)
	if err != nil {
		return fmt.Errorf("failed to create pip install command:\n%w", err)
	}

	if err := cmd.Run(env); err != nil {
		return fmt.Errorf("failed to install dependencies from %#q:\n%w", pyprojectPath, err)
	}

	return nil
}

// BuildNativePytestArgs constructs the full pytest argument list from the config.
// Test paths are glob-expanded relative to the working directory. Extra args are passed
// verbatim after placeholder substitution. The --junit-xml flag is appended automatically
// when requested via CLI.
func BuildNativePytestArgs(pytestConfig *projectconfig.PytestConfig, options *ImageTestOptions) []string {
	absImagePath, err := filepath.Abs(options.ImagePath)
	if err != nil {
		absImagePath = options.ImagePath
	}

	args := make([]string, 0, len(pytestConfig.TestPaths)+len(pytestConfig.ExtraArgs))

	// Expand test paths (glob patterns resolved relative to working dir).
	for _, testPath := range pytestConfig.TestPaths {
		if containsGlobMeta(testPath) {
			args = append(args, expandGlob(testPath, pytestConfig.WorkingDir)...)
		} else {
			args = append(args, testPath)
		}
	}

	// Substitute placeholders in extra args (never glob-expanded).
	for _, arg := range pytestConfig.ExtraArgs {
		args = append(args, strings.ReplaceAll(arg, imagePlaceholder, absImagePath))
	}

	// Append --junit-xml when requested via CLI.
	if options.JUnitXMLPath != "" {
		args = append(args, "--junit-xml", options.JUnitXMLPath)
	}

	return args
}

// containsGlobMeta returns true if the string contains glob metacharacters.
func containsGlobMeta(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

// expandGlob expands a glob pattern relative to workingDir. If the pattern matches no
// files, the original pattern is returned unchanged (letting pytest report the error).
func expandGlob(pattern string, workingDir string) []string {
	// Resolve the pattern relative to the working directory.
	absPattern := pattern
	if workingDir != "" && !filepath.IsAbs(pattern) {
		absPattern = filepath.Join(workingDir, pattern)
	}

	matches, err := filepath.Glob(absPattern)
	if err != nil || len(matches) == 0 {
		// Return the original (relative) pattern so pytest can report the error.
		return []string{pattern}
	}

	// Convert back to paths relative to the working directory so pytest sees them
	// the same way it would with shell expansion.
	result := make([]string, 0, len(matches))

	for _, match := range matches {
		if workingDir != "" {
			rel, relErr := filepath.Rel(workingDir, match)
			if relErr == nil {
				result = append(result, rel)

				continue
			}
		}

		result = append(result, match)
	}

	return result
}
