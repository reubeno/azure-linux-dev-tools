// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tmt

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
)

const candidateRPMPrepareStepName = "azldev-candidate-rpms"

func runArgs(
	plan string, request testframework.RunRequest, outputDir string, hardware []string,
) []string {
	args := []string{"run", "--all", "--keep", "--workdir-root", outputDir, "plan"}
	if plan != "" {
		args = append(args, "--name", plan)
	}

	args = append(args, "provision", "--how", nativeProvisioner(request.Execution.Strategy), "--become")

	switch request.Execution.Strategy {
	case testframework.VM:
		args = append(args, "--image", request.ImagePath)
		for _, constraint := range hardware {
			args = append(args, "--hardware", constraint)
		}
	case testframework.Container:
		args = append(args, "--image", request.ImagePath)
	case testframework.SSH:
		args = append(args, "--guest", request.Execution.SSHHost, "--user", request.Execution.SSHUser)
		if request.Execution.SSHKey != "" {
			args = append(args, "--key", request.Execution.SSHKey)
		}

		if request.Execution.SSHPort != 0 {
			args = append(args, "--port", strconv.Itoa(request.Execution.SSHPort))
		}
	case testframework.Host:
	}

	// Inserting a separate step avoids inheriting a plan preparation's 'when'
	// condition. Image-only tests must leave preparation completely unchanged.
	if len(request.RPMPaths) != 0 {
		args = append(args, "prepare", "--insert", "--how", "install", "--name", candidateRPMPrepareStepName)
		for _, rpm := range request.RPMPaths {
			args = append(args, "--package", rpm)
		}
	}

	return args
}

func nativeProvisioner(strategy testframework.Strategy) string {
	switch strategy {
	case testframework.VM:
		return "virtual"
	case testframework.Container:
		return "container"
	case testframework.SSH:
		return "connect"
	case testframework.Host:
		return "local"
	default:
		return ""
	}
}

func commandOutput(ctx opctx.Ctx, dir, program string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("TMT command cancelled:\n%w", err)
	}

	if ctx.DryRun() {
		return "", nil
	}

	command := exec.CommandContext(ctx, program, args...)
	command.Dir = dir

	var stderr bytes.Buffer

	command.Stderr = &stderr

	wrapped, err := ctx.Command(command)
	if err != nil {
		return "", fmt.Errorf("create TMT command:\n%w", err)
	}

	output, err := wrapped.RunAndGetOutput(ctx)
	if err != nil {
		return "", fmt.Errorf("run %#q (%s):\n%w", program, strings.TrimSpace(stderr.String()), err)
	}

	return output, nil
}

func runTMTCommand(
	ctx opctx.Ctx, dir, pluginDir, program string, strategy testframework.Strategy, args ...string,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("TMT command cancelled:\n%w", err)
	}

	if ctx.DryRun() {
		return nil
	}

	command := exec.CommandContext(ctx, program, args...)
	command.Dir = dir
	command.Stdout = os.Stdout
	command.Env = append(os.Environ(), commandEnv(ctx, pluginDir, strategy)...)

	var stderr bytes.Buffer

	command.Stderr = io.MultiWriter(os.Stderr, &stderr)

	wrapped, err := ctx.Command(command)
	if err != nil {
		return fmt.Errorf("create TMT command:\n%w", err)
	}

	if err := wrapped.Run(ctx); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("TMT command cancelled:\n%w", ctx.Err())
		}

		var exitError interface{ ExitCode() int }
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return fmt.Errorf("%w (%s):\n%w", testframework.ErrTestFailed, strings.TrimSpace(stderr.String()), err)
		}

		return fmt.Errorf("run TMT (%s):\n%w", strings.TrimSpace(stderr.String()), err)
	}

	return nil
}

func commandEnv(ctx opctx.Ctx, pluginDir string, strategy testframework.Strategy) []string {
	var vars []string

	if pluginDir != "" {
		vars = append(vars, "TMT_PLUGINS="+pluginDir)
	}

	if ctx.OSEnv().Getenv("TMT_BOOT_TIMEOUT") == "" {
		vars = append(vars, "TMT_BOOT_TIMEOUT=300")
	}

	if strategy == testframework.Host {
		vars = append(vars, "TMT_FEELING_SAFE=1")
	}

	return vars
}
