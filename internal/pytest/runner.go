// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package pytest runs host-side pytest suites against materialized test inputs.
package pytest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/testsource"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/pythonenv"
	"github.com/pelletier/go-toml/v2"
)

const exitNoTests = 5

type config struct {
	WorkingDir string                          `toml:"working-dir"`
	TestPaths  []string                        `toml:"test-paths"`
	ExtraArgs  []string                        `toml:"extra-args"`
	Install    projectconfig.PytestInstallMode `toml:"install"`
	Source     *projectconfig.GitSourceConfig  `toml:"source"`
}

// Runner is an independently configured pytest adapter.
type Runner struct {
	name   string
	config config
}

var _ testframework.Runner = (*Runner)(nil)

// New decodes locally consumed configuration without rejecting external metadata.
func New(name string, raw map[string]any) (*Runner, error) {
	if err := testframework.ValidateName(name); err != nil {
		return nil, fmt.Errorf("pytest test name:\n%w", err)
	}

	consumed := make(map[string]any)
	for _, key := range []string{"working-dir", "test-paths", "extra-args", "install", "source"} {
		if value, ok := raw[key]; ok {
			consumed[key] = value
		}
	}

	data, err := toml.Marshal(consumed)
	if err != nil {
		return nil, fmt.Errorf("encode pytest configuration:\n%w", err)
	}

	runner := &Runner{name: name}
	if err := toml.Unmarshal(data, &runner.config); err != nil {
		return nil, fmt.Errorf("decode pytest configuration:\n%w", err)
	}

	if runner.config.Install == "" {
		runner.config.Install = projectconfig.PytestInstallNone
	}

	switch runner.config.Install {
	case projectconfig.PytestInstallNone, projectconfig.PytestInstallPyproject, projectconfig.PytestInstallRequirements:
	default:
		return nil, fmt.Errorf("unsupported pytest install mode %#q", runner.config.Install)
	}

	if runner.config.Source != nil {
		if err := runner.config.Source.Validate("pytest.source"); err != nil {
			return nil, fmt.Errorf("pytest source:\n%w", err)
		}

		if filepath.IsAbs(runner.config.WorkingDir) || escapesCheckout(runner.config.WorkingDir) {
			return nil, errors.New("pytest 'working-dir' must be relative to its source checkout")
		}
	} else if runner.config.WorkingDir != "" && !filepath.IsAbs(runner.config.WorkingDir) {
		return nil, errors.New("pytest 'working-dir' must be absolute when no source is configured")
	}

	return runner, nil
}

func escapesCheckout(path string) bool {
	clean := filepath.Clean(path)

	return clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

// Requirements rejects strategies which pytest does not provision.
func (r *Runner) Requirements(spec testframework.ExecutionSpec) (testframework.Requirements, error) {
	if err := spec.Validate(); err != nil {
		return testframework.Requirements{}, fmt.Errorf("pytest execution:\n%w", err)
	}

	if spec.Strategy != testframework.Host {
		return testframework.Requirements{},
			fmt.Errorf("pytest supports only the 'host' execution strategy, not %#q", spec.Strategy)
	}

	return testframework.Requirements{
		PackagesSupported: true, ImageFormats: []string{"qcow2", "raw", "vhd", "vhdx", "oci"},
	}, nil
}

// Run executes pytest and retains its native report and normalized exit status.
func (r *Runner) Run(ctx opctx.Ctx, request testframework.RunRequest) (result testframework.RunResult, err error) {
	start := time.Now()
	result = testframework.RunResult{
		Name: r.name, Framework: "pytest", Strategy: request.Execution.Strategy,
		Status: testframework.Errored, OutputDir: request.WorkDir,
	}

	defer func() {
		result.Duration = time.Since(start)
		if err != nil {
			result.Error = err.Error()
			if ctx.Err() != nil {
				result.Status = testframework.Cancelled
			}
		}
	}()

	if _, err = r.Requirements(request.Execution); err != nil {
		return result, err
	}

	if err = validateRequest(request); err != nil {
		return result, err
	}

	if ctx.DryRun() {
		result.Status = testframework.Planned

		return result, nil
	}

	err = r.execute(ctx, request, &result)

	return result, err
}

func validateRequest(request testframework.RunRequest) error {
	if !filepath.IsAbs(request.WorkDir) || !filepath.IsAbs(request.CacheDir) {
		return errors.New("pytest requires absolute work and cache directories")
	}

	if request.JUnitPath != "" && !filepath.IsAbs(request.JUnitPath) {
		return errors.New("pytest JUnit path must be absolute")
	}

	if len(request.RPMPaths) > 0 && request.InputsPath == "" {
		return errors.New("pytest candidate packages require an inputs manifest")
	}

	if request.InputsPath != "" && !filepath.IsAbs(request.InputsPath) {
		return errors.New("pytest inputs manifest path must be absolute")
	}

	return nil
}

func (r *Runner) execute(ctx opctx.Ctx, request testframework.RunRequest, result *testframework.RunResult) error {
	workingDir, err := r.workingDir(ctx, request)
	if err != nil {
		return err
	}

	if err := fileutils.MkdirAll(ctx.FS(), request.WorkDir); err != nil {
		return fmt.Errorf("create pytest work directory:\n%w", err)
	}

	if request.JUnitPath != "" {
		if err := fileutils.MkdirAll(ctx.FS(), filepath.Dir(request.JUnitPath)); err != nil {
			return fmt.Errorf("create pytest report directory:\n%w", err)
		}
	}

	key := pythonenv.Key("pytest", r.name, workingDir, string(r.config.Install))

	python, err := pythonenv.Ensure(ctx, filepath.Join(request.CacheDir, "python", key))
	if err != nil {
		return fmt.Errorf("create pytest Python environment:\n%w", err)
	}

	if err := r.install(ctx, python, workingDir); err != nil {
		return err
	}

	args, err := r.arguments(ctx.FS(), workingDir, request)
	if err != nil {
		return err
	}

	if ctx.Verbose() {
		args = append(args, "--log-cli-level=DEBUG")
	}

	return runAndReport(ctx, python, workingDir, args, request, result)
}

func runAndReport(
	ctx opctx.Ctx, python, workingDir string, args []string,
	request testframework.RunRequest, result *testframework.RunResult,
) error {
	if request.JUnitPath != "" {
		if removeErr := ctx.FS().Remove(request.JUnitPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("remove previous pytest report:\n%w", removeErr)
		}
	}

	err := invoke(ctx, python, workingDir, request.InputsPath, args)
	if request.JUnitPath != "" {
		exists, reportErr := fileutils.Exists(ctx.FS(), request.JUnitPath)
		if reportErr != nil {
			return errors.Join(err, fmt.Errorf("check pytest report:\n%w", reportErr))
		}

		if !exists {
			return errors.Join(err, fmt.Errorf("pytest did not produce the requested JUnit report %#q", request.JUnitPath))
		}

		result.JUnitPath = request.JUnitPath
	}

	if err == nil {
		result.Status = testframework.Passed

		return nil
	}

	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) {
		switch exit.ExitCode() {
		case 1:
			result.Status = testframework.Failed
			err = errors.Join(testframework.ErrTestFailed, err)
		case exitNoTests:
			result.Status = testframework.Skipped

			return nil
		}
	}

	return err
}

func (r *Runner) workingDir(ctx opctx.Ctx, request testframework.RunRequest) (string, error) {
	dir := r.config.WorkingDir
	if r.config.Source != nil || request.Execution.SourceDir != "" {
		root, err := testsource.Materialize(ctx, r.config.Source, request.Execution.SourceDir, request.CacheDir)
		if err != nil {
			return "", fmt.Errorf("materialize pytest source:\n%w", err)
		}

		if filepath.IsAbs(dir) {
			dir = root
		} else {
			dir = filepath.Join(root, dir)
		}
	}

	if dir == "" {
		if r.config.Install != projectconfig.PytestInstallNone {
			return "", errors.New("pytest dependency installation requires 'working-dir'")
		}

		return "", nil
	}

	exists, err := fileutils.DirExists(ctx.FS(), dir)
	if err != nil {
		return "", fmt.Errorf("check pytest working directory:\n%w", err)
	}

	if !exists {
		return "", fmt.Errorf("pytest working directory %#q does not exist", dir)
	}

	return dir, nil
}

func (r *Runner) install(ctx opctx.Ctx, python, dir string) error {
	var (
		filename string
		args     []string
	)

	switch r.config.Install {
	case projectconfig.PytestInstallNone:
		return nil
	case projectconfig.PytestInstallPyproject:
		filename = "pyproject.toml"
		args = []string{"-e", dir}
	case projectconfig.PytestInstallRequirements:
		filename = "requirements.txt"
		args = []string{"-r", filepath.Join(dir, filename)}
	}

	exists, err := fileutils.Exists(ctx.FS(), filepath.Join(dir, filename))
	if err != nil {
		return fmt.Errorf("check pytest dependency file:\n%w", err)
	}

	if !exists {
		return fmt.Errorf("pytest install mode %#q requires %#q in %#q", r.config.Install, filename, dir)
	}

	if err := pythonenv.Install(ctx, python, dir, args...); err != nil {
		return fmt.Errorf("install pytest dependencies:\n%w", err)
	}

	return nil
}

func (r *Runner) arguments(fs opctx.FS, workingDir string, request testframework.RunRequest) ([]string, error) {
	properties := request.Properties
	if properties == nil {
		properties = map[string]string{}
	}

	encoded, err := json.Marshal(properties)
	if err != nil {
		return nil, fmt.Errorf("encode test properties:\n%w", err)
	}

	replacer := strings.NewReplacer(
		"{image-path}", request.ImagePath, "{image-name}", request.SubjectName,
		"{subject-name}", request.SubjectName, "{inputs-path}", request.InputsPath,
		"{capabilities}", strings.Join(request.Capabilities, ","), "{properties}", string(encoded),
	)
	args := []string{"-m", "pytest"}

	for _, pattern := range r.config.TestPaths {
		args = append(args, expandGlob(fs, pattern, workingDir)...)
	}

	for _, arg := range r.config.ExtraArgs {
		args = append(args, replacer.Replace(arg))
	}

	if request.JUnitPath != "" {
		args = append(args, "--junit-xml", request.JUnitPath)
	}

	return args, nil
}

func expandGlob(fs opctx.FS, pattern, dir string) []string {
	full := pattern
	if dir != "" && !filepath.IsAbs(pattern) {
		full = filepath.Join(dir, pattern)
	}

	matches, err := fileutils.Glob(fs, full, doublestar.WithFilesOnly(), doublestar.WithFailOnIOErrors())
	if err != nil {
		slog.Warn("Failed to expand pytest glob", "pattern", pattern, "error", err)
	}

	if len(matches) == 0 || err != nil {
		return []string{pattern}
	}

	for index, match := range matches {
		if dir != "" {
			if relative, err := filepath.Rel(dir, match); err == nil {
				matches[index] = relative
			}
		}
	}

	return matches
}

func invoke(ctx opctx.Ctx, python, dir, inputs string, args []string) error {
	raw := exec.CommandContext(ctx, python, args...)
	raw.Dir = dir
	raw.Stdout = os.Stdout
	raw.Env = append(raw.Environ(), "AZLDEV_TEST_INPUTS="+inputs)

	var stderr bytes.Buffer

	raw.Stderr = io.MultiWriter(os.Stderr, &stderr)

	command, err := ctx.Command(raw)
	if err != nil {
		return fmt.Errorf("create pytest command:\n%w", err)
	}

	if err := command.Run(ctx); err != nil {
		return fmt.Errorf("pytest execution (%s):\n%w", strings.TrimSpace(stderr.String()), err)
	}

	return nil
}
