// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lisa

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/testsource"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/externalcmd"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/prereqs"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/pythonenv"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/qemu"
)

// Requirements describes the actual environment booted by LISA's QEMU platform.
func (r *Runner) Requirements(spec testframework.ExecutionSpec) (testframework.Requirements, error) {
	if err := spec.Validate(); err != nil {
		return testframework.Requirements{}, fmt.Errorf("LISA execution:\n%w", err)
	}

	if spec.Strategy != testframework.VM {
		return testframework.Requirements{},
			fmt.Errorf("LISA supports only the 'vm' execution strategy, not %#q", spec.Strategy)
	}

	if spec.SourceDir != "" || spec.Plan != "" {
		return testframework.Requirements{}, errors.New("LISA does not consume test-source or TMT-plan overrides")
	}

	if r.config.Source == nil && spec.FrameworkDir == "" {
		return testframework.Requirements{}, errors.New("LISA requires a pinned framework source or framework directory")
	}

	return testframework.Requirements{
		ImageRequired: true, ImageFormats: qemu.DiskFormats(), LiveImage: true, PackagesSupported: true,
	}, nil
}

// Run provisions LISA, installs candidate RPMs on connected nodes, and runs selectors.
func (r *Runner) Run(ctx opctx.Ctx, request testframework.RunRequest) (result testframework.RunResult, err error) {
	start := time.Now()

	if request.JUnitPath == "" {
		request.JUnitPath = filepath.Join(request.WorkDir, "lisa.junit.xml")
	}

	result = testframework.RunResult{
		Name: r.name, Framework: "lisa", Strategy: request.Execution.Strategy,
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

	if err = ctx.Err(); err != nil {
		return result, fmt.Errorf("LISA execution cancelled:\n%w", err)
	}

	if ctx.DryRun() {
		result.Status = testframework.Planned

		return result, nil
	}

	if err = testsource.CheckDestination(request.Execution.FrameworkDir, request.WorkDir); err != nil {
		return result, fmt.Errorf("LISA run directory must not overlap the framework checkout:\n%w", err)
	}

	if err = validateFrameworkDirectory(ctx, request.Execution.FrameworkDir); err != nil {
		return result, err
	}

	if err = fileutils.MkdirAll(ctx.FS(), request.WorkDir); err != nil {
		return result, fmt.Errorf("create LISA work directory:\n%w", err)
	}

	privateDir := filepath.Join(request.WorkDir, "private-"+rand.Text())
	if err = ctx.FS().Mkdir(privateDir, fileperms.PrivateDir); err != nil {
		return result, fmt.Errorf("create private LISA run directory:\n%w", err)
	}

	defer func() {
		if cleanupErr := ctx.FS().RemoveAll(privateDir); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove private LISA run files:\n%w", cleanupErr))
			result.Status = testframework.Errored
		}
	}()

	frameworkDir, err := testsource.Materialize(ctx, r.config.Source, request.Execution.FrameworkDir,
		filepath.Join(privateDir, "framework"))
	if err != nil {
		return result, fmt.Errorf("materialize LISA framework:\n%w", err)
	}

	err = r.execute(ctx, frameworkDir, privateDir, request, &result)

	return result, err
}

func validateFrameworkDirectory(ctx opctx.Ctx, dir string) error {
	if dir == "" {
		return nil
	}

	exists, err := fileutils.DirExists(ctx.FS(), dir)
	if err != nil {
		return fmt.Errorf("check LISA framework:\n%w", err)
	}

	if !exists {
		return fmt.Errorf("LISA framework directory %#q does not exist", dir)
	}

	return nil
}

func (r *Runner) execute(
	ctx opctx.Ctx, frameworkDir, privateDir string,
	request testframework.RunRequest, result *testframework.RunResult,
) error {
	imagePath, err := qemu.EnsureQcow2(ctx, request.ImagePath(), filepath.Join(privateDir, "image.qcow2"))
	if err != nil {
		return fmt.Errorf("prepare LISA disk image:\n%w", err)
	}

	image := *request.Image
	image.Path, image.Format, image.SHA256 = imagePath, "qcow2", ""
	request.Image = &image

	python, err := r.preparePython(ctx, frameworkDir)
	if err != nil {
		return err
	}

	keyPath := filepath.Join(privateDir, "id_rsa")

	if err := prereqs.RequireExecutable(ctx, "ssh-keygen", nil); err != nil {
		return fmt.Errorf("LISA admin key prerequisite:\n%w", err)
	}

	keyArgs := []string{"-t", "rsa", "-b", "4096", "-f", keyPath, "-N", "", "-q"}
	if err := externalcmd.Run(ctx, privateDir, "ssh-keygen", keyArgs...); err != nil {
		return fmt.Errorf("generate LISA admin key:\n%w", err)
	}

	if err := fileutils.MkdirAll(ctx.FS(), filepath.Dir(request.JUnitPath)); err != nil {
		return fmt.Errorf("create LISA report directory:\n%w", err)
	}

	runbook, err := r.runbook(frameworkDir, keyPath, request)
	if err != nil {
		return err
	}

	if err := writeCandidateScript(ctx.FS(), privateDir, request.RPMPaths()); err != nil {
		return err
	}

	runbookPath := filepath.Join(privateDir, "runbook.yml")
	if err := fileutils.WriteFile(ctx.FS(), runbookPath, runbook, fileperms.PrivateFile); err != nil {
		return fmt.Errorf("write generated LISA runbook:\n%w", err)
	}

	args, err := r.arguments(runbookPath, request)
	if err != nil {
		return err
	}

	if removeErr := ctx.FS().Remove(request.JUnitPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return fmt.Errorf("remove previous LISA report:\n%w", removeErr)
	}

	runErr := externalcmd.RunInterruptible(ctx, filepath.Join(frameworkDir, "lisa"),
		filepath.Join(filepath.Dir(python), "lisa"), time.Minute, args...)

	return finishRun(ctx, request.JUnitPath, runErr, result)
}

func finishRun(ctx opctx.Ctx, junitPath string, runErr error, result *testframework.RunResult) error {
	exists, err := fileutils.Exists(ctx.FS(), junitPath)
	if err != nil {
		return errors.Join(runErr, fmt.Errorf("check LISA report:\n%w", err))
	}

	if exists {
		result.JUnitPath = junitPath
	}

	cases, reportErr := testframework.ReadJUnit(ctx.FS(), junitPath)
	result.Cases = cases

	if reportErr != nil {
		return errors.Join(runErr, reportErr)
	}

	native := nativeExitStatus(runErr, cases)

	result.Status, err = testframework.ResolveOutcome(native, runErr, cases)
	if err != nil {
		return fmt.Errorf("LISA outcome:\n%w", err)
	}

	return nil
}

func nativeExitStatus(runErr error, cases []testframework.CaseResult) testframework.Status {
	if runErr == nil {
		return testframework.Passed
	}

	failures := 0

	for _, testcase := range cases {
		if testcase.Status == testframework.Failed {
			failures++
		}
	}

	var exit interface{ ExitCode() int }
	if failures > 0 && errors.As(runErr, &exit) && exit.ExitCode() == failures {
		return testframework.Failed
	}

	return testframework.Errored
}

func validateRequest(request testframework.RunRequest) error {
	if err := request.ValidateInputs(); err != nil {
		return fmt.Errorf("LISA inputs:\n%w", err)
	}

	if !filepath.IsAbs(request.WorkDir) || !filepath.IsAbs(request.JUnitPath) {
		return errors.New("LISA requires absolute work and JUnit paths")
	}

	if !filepath.IsAbs(request.ImagePath()) || request.Image == nil ||
		!slices.Contains(qemu.DiskFormats(), request.Image.Format) {
		return errors.New("LISA requires an absolute path to a supported disk image")
	}

	for _, path := range request.RPMPaths() {
		if !filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".rpm") {
			return fmt.Errorf("LISA candidate package must be an absolute RPM path: %#q", path)
		}
	}

	return nil
}

func writeCandidateScript(files opctx.FS, privateDir string, rpms []string) error {
	if len(rpms) == 0 {
		return nil
	}

	if err := fileutils.WriteFile(files, filepath.Join(privateDir, candidateScriptName),
		[]byte(candidateInstallScript), fileperms.PrivateFile); err != nil {
		return fmt.Errorf("write candidate installation script:\n%w", err)
	}

	return nil
}

func (r *Runner) preparePython(ctx opctx.Ctx, frameworkDir string) (string, error) {
	// The editable install points at this private copy. Sharing its environment
	// would let concurrent runs redirect each other's imports or remove them.
	python, err := pythonenv.Create(ctx, filepath.Join(filepath.Dir(frameworkDir), "python"))
	if err != nil {
		return "", fmt.Errorf("create LISA Python environment:\n%w", err)
	}

	if len(r.config.PipPreInstall) > 0 {
		if err := pythonenv.Install(ctx, python, frameworkDir, r.config.PipPreInstall...); err != nil {
			return "", fmt.Errorf("install LISA prerequisites:\n%w", err)
		}
	}

	target := frameworkDir
	if len(r.config.PipExtras) > 0 {
		target += "[" + strings.Join(r.config.PipExtras, ",") + "]"
	}

	if err := pythonenv.Install(ctx, python, frameworkDir, "-e", target); err != nil {
		return "", fmt.Errorf("install LISA framework:\n%w", err)
	}

	return python, nil
}

func (r *Runner) arguments(runbookPath string, request testframework.RunRequest) ([]string, error) {
	properties := request.Properties
	if properties == nil {
		properties = map[string]string{}
	}

	encoded, err := json.Marshal(properties)
	if err != nil {
		return nil, fmt.Errorf("encode LISA test properties:\n%w", err)
	}

	replacer := strings.NewReplacer(
		"{image-path}", request.ImagePath(), "{image-name}", request.SubjectName,
		"{subject-name}", request.SubjectName, "{inputs-path}", request.InputsPath,
		"{capabilities}", strings.Join(request.Capabilities, ","), "{properties}", string(encoded),
	)
	args := []string{"-r", runbookPath, "--log_path", request.WorkDir, "--working_path", request.WorkDir}

	for _, arg := range r.config.ExtraArgs {
		args = append(args, replacer.Replace(arg))
	}

	return args, nil
}
