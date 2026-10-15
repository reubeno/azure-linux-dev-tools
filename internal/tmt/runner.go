// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tmt

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/testsource"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/prereqs"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/pythonenv"
)

//go:embed requirements.txt
var requirements []byte

// Run executes a plan without taking ownership of a caller's source tree or SSH machine.
func (r *Runner) Run(ctx opctx.Ctx, request testframework.RunRequest) (testframework.RunResult, error) {
	started := time.Now()
	result := testframework.RunResult{
		Name: r.name, Framework: "tmt", Strategy: request.Execution.Strategy,
		OutputDir: filepath.Join(request.WorkDir, "tmt"),
	}

	err := r.run(ctx, request, result.OutputDir)
	result.Duration = time.Since(started)

	switch {
	case err == nil && ctx.DryRun():
		result.Status = testframework.Planned
	case err == nil:
		result.Status = testframework.Passed
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), ctx.Err() != nil:
		result.Status = testframework.Cancelled
	case errors.Is(err, testframework.ErrTestFailed):
		result.Status = testframework.Failed
	default:
		result.Status = testframework.Errored
	}

	if err != nil {
		result.Error = err.Error()
	}

	// Native report plugins belong to the plan. The default display reporter
	// does not produce JUnit, so do not advertise the requested path as a report.
	return result, err
}

func (r *Runner) run(ctx opctx.Ctx, request testframework.RunRequest, outputDir string) (err error) {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("TMT execution cancelled:\n%w", err)
	}

	if err := r.validateRequest(request); err != nil {
		return err
	}

	if ctx.DryRun() {
		return nil
	}

	if err := validatePrerequisites(ctx, request); err != nil {
		return err
	}

	privateDir, err := fileutils.MkdirTempInTempDir(ctx.FS(), "azldev-tmt-")
	if err != nil {
		return fmt.Errorf("create private TMT runtime:\n%w", err)
	}

	defer func() {
		if cleanupErr := ctx.FS().RemoveAll(privateDir); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove private TMT runtime:\n%w", cleanupErr))
		}
	}()

	runDir, err := testsource.Materialize(ctx, r.config.Source, request.Execution.SourceDir,
		filepath.Join(privateDir, "source"))
	if err != nil {
		return fmt.Errorf("materialize TMT source:\n%w", err)
	}

	if err := validateSource(ctx, runDir); err != nil {
		return err
	}

	program, err := prepareEnvironment(ctx, request, privateDir)
	if err != nil {
		return err
	}

	pluginDir, err := prepareRunDir(ctx, request)
	if err != nil {
		return err
	}

	args, err := r.prepareArguments(ctx, request, runDir, program, outputDir)
	if err != nil {
		return err
	}

	if err := runTMTCommand(ctx, runDir, pluginDir, program, request.Execution.Strategy, args...); err != nil {
		return fmt.Errorf("run TMT (artifacts: %#q):\n%w", outputDir, err)
	}

	return nil
}

func (r *Runner) prepareArguments(ctx opctx.Ctx, request testframework.RunRequest,
	runDir, program, outputDir string,
) ([]string, error) {
	var (
		hardware []string
		err      error
	)

	if request.Execution.Strategy == testframework.VM {
		hardware, err = resolvedPlanHardwareArgs(ctx, runDir, program, r.plan(request.Execution))
		if err != nil {
			return nil, fmt.Errorf("resolve TMT plan hardware:\n%w", err)
		}
	}

	imageArgument := request.ImagePath()
	if request.Execution.Strategy == testframework.Container && filepath.IsAbs(imageArgument) {
		imageArgument, err = importContainerImage(ctx, imageArgument)
		if err != nil {
			return nil, err
		}
	}

	return runArgs(r.plan(request.Execution), request, outputDir, hardware, imageArgument), nil
}

func (r *Runner) validateRequest(request testframework.RunRequest) error {
	requirements, err := r.Requirements(request.Execution)
	if err != nil {
		return err
	}

	for _, directory := range []struct{ name, path string }{
		{"work", request.WorkDir}, {"cache", request.CacheDir},
	} {
		if !filepath.IsAbs(directory.path) {
			return fmt.Errorf("TMT %s directory must be absolute", directory.name)
		}

		if directory.name == "work" && request.Execution.SourceDir != "" &&
			dirsOverlap(request.Execution.SourceDir, directory.path) {
			return fmt.Errorf("TMT source and %s directories must not overlap", directory.name)
		}
	}

	if err := validateImage(request, requirements); err != nil {
		return err
	}

	for _, rpm := range request.RPMPaths() {
		if !filepath.IsAbs(rpm) {
			return fmt.Errorf("candidate RPM path must be absolute: %#q", rpm)
		}
	}

	return nil
}

func validateImage(request testframework.RunRequest, requirements testframework.Requirements) error {
	if !requirements.ImageRequired {
		if request.Image != nil {
			return fmt.Errorf("TMT strategy %#q does not accept an image", request.Execution.Strategy)
		}

		return nil
	}

	if request.Image != nil && !slices.Contains(requirements.ImageFormats, request.Image.Format) {
		return fmt.Errorf("TMT strategy %#q does not support image format %#q",
			request.Execution.Strategy, request.Image.Format)
	}

	if !filepath.IsAbs(request.ImagePath()) &&
		(request.Execution.Strategy != testframework.Container || !isContainerImageID(request.ImagePath())) {
		return errors.New("TMT image path must be absolute, or a local image ID for strategy 'container'")
	}

	return nil
}

func validateFiles(ctx opctx.Ctx, request testframework.RunRequest) error {
	paths := append([]string{}, request.RPMPaths()...)
	if filepath.IsAbs(request.ImagePath()) {
		paths = append(paths, request.ImagePath())
	}

	if request.Execution.SSHKey != "" {
		paths = append(paths, request.Execution.SSHKey)
	}

	for _, path := range paths {
		info, err := ctx.FS().Stat(path)
		if err != nil {
			return fmt.Errorf("TMT input file %#q:\n%w", path, err)
		}

		if !info.Mode().IsRegular() {
			return fmt.Errorf("TMT input %#q must be a regular file", path)
		}
	}

	return nil
}

func validatePrerequisites(ctx opctx.Ctx, request testframework.RunRequest) error {
	if request.Execution.Strategy == testframework.Host {
		if err := prereqs.RequireAzureLinux4(ctx); err != nil {
			return fmt.Errorf("validate local TMT provisioner host:\n%w", err)
		}

		if err := prereqs.RequireExecutable(ctx, "sudo", nil); err != nil {
			return fmt.Errorf("sudo is required for TMT strategy 'host':\n%w", err)
		}
	}

	if request.Execution.Strategy == testframework.Container {
		if err := prereqs.RequireExecutable(ctx, "podman", nil); err != nil {
			return fmt.Errorf("podman is required for TMT strategy 'container':\n%w", err)
		}
	}

	return validateFiles(ctx, request)
}

func validateSource(ctx opctx.Ctx, sourceDir string) error {
	marker := filepath.Join(sourceDir, ".fmf", "version")

	info, err := ctx.FS().Stat(marker)
	if err != nil {
		return fmt.Errorf("check fmf metadata %#q:\n%w", marker, err)
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("fmf metadata %#q must be a regular file", marker)
	}

	return nil
}

func prepareEnvironment(ctx opctx.Ctx, request testframework.RunRequest, privateDir string) (string, error) {
	requirement := "tmt"
	if request.Execution.Strategy == testframework.VM {
		requirement = "tmt[provision-virtual]"
	}

	venvDir := filepath.Join(privateDir, "venv")

	python, err := pythonenv.Create(ctx, venvDir)
	if err != nil {
		return "", fmt.Errorf("create TMT environment:\n%w", err)
	}

	constraints := filepath.Join(privateDir, "requirements.txt")
	if err := fileutils.WriteFile(ctx.FS(), constraints, requirements, fileperms.PrivateFile); err != nil {
		return "", fmt.Errorf("write TMT dependency constraints:\n%w", err)
	}

	if err := pythonenv.Install(ctx, python, "", "--constraint", constraints, requirement); err != nil {
		return "", fmt.Errorf("install TMT:\n%w", err)
	}

	if err := pythonenv.Freeze(ctx, python, filepath.Join(request.WorkDir, "python-environment.txt")); err != nil {
		return "", fmt.Errorf("record TMT runtime:\n%w", err)
	}

	return filepath.Join(venvDir, "bin", "tmt"), nil
}

func prepareRunDir(ctx opctx.Ctx, request testframework.RunRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("prepare TMT run cancelled:\n%w", err)
	}

	if err := fileutils.MkdirAll(ctx.FS(), request.WorkDir); err != nil {
		return "", fmt.Errorf("create TMT work directory:\n%w", err)
	}

	if request.Execution.Strategy != testframework.VM {
		return "", nil
	}

	return writeTestcloudPlugin(ctx, request.WorkDir)
}

func importContainerImage(ctx opctx.Ctx, imagePath string) (string, error) {
	// TMT's container provisioner needs an image ID/name, not an OCI archive.
	// Pulling from the local transport avoids a registry lookup or mutable tag.
	output, err := commandOutput(ctx, "", "podman", "pull", "--quiet", "oci-archive:"+imagePath)
	if err != nil {
		return "", fmt.Errorf("import TMT container image:\n%w", err)
	}

	image := strings.TrimSpace(output)
	if !strings.HasPrefix(image, "sha256:") {
		image = "sha256:" + image
	}

	if !isContainerImageID(image) {
		return "", errors.New("podman did not return a single imported image ID")
	}

	return image, nil
}

func isContainerImageID(image string) bool {
	if !strings.HasPrefix(image, "sha256:") {
		return false
	}

	digest := strings.TrimPrefix(image, "sha256:")

	const digestLength = 64

	if len(digest) != digestLength {
		return false
	}

	for _, character := range digest {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}

	return true
}

func dirsOverlap(first, second string) bool {
	return isAncestorOrSame(first, second) || isAncestorOrSame(second, first)
}

func isAncestorOrSame(ancestor, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(ancestor), filepath.Clean(path))
	if err != nil {
		return false
	}

	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
