// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package pythonenv manages isolated Python installations for external tools.
package pythonenv

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/externalcmd"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/prereqs"
)

// Create creates a fresh, caller-owned virtual environment and returns its Python executable.
// Existing paths are rejected, including incomplete environments. The caller must remove
// the environment after use, including when a subsequent dependency installation fails.
func Create(ctx opctx.Ctx, dir string) (python string, err error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("create Python environment cancelled:\n%w", err)
	}

	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("python environment directory must be absolute: %#q", dir)
	}

	python = filepath.Join(dir, "bin", "python3")
	if ctx.DryRun() {
		return python, nil
	}

	if err := prereqs.RequireExecutable(ctx, "python3", nil); err != nil {
		return "", fmt.Errorf("python prerequisite:\n%w", err)
	}

	if err := fileutils.MkdirAll(ctx.FS(), filepath.Dir(dir)); err != nil {
		return "", fmt.Errorf("create Python environment parent:\n%w", err)
	}

	if err := ctx.FS().Mkdir(dir, fileperms.PrivateDir); err != nil {
		return "", fmt.Errorf("create fresh Python environment %#q:\n%w", dir, err)
	}

	defer func() {
		if err != nil {
			err = errors.Join(err, ctx.FS().RemoveAll(dir))
		}
	}()

	if err := externalcmd.Run(ctx, "", "python3", "-m", "venv", dir); err != nil {
		return "", fmt.Errorf("create Python environment:\n%w", err)
	}

	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("create Python environment cancelled:\n%w", err)
	}

	return python, nil
}

// Existing returns a caller-prepared virtual environment without creating or installing anything.
// Its packages must be validated by the caller before use; a Python binary alone is not a venv.
func Existing(ctx opctx.Ctx, dir string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("check prepared Python environment cancelled:\n%w", err)
	}

	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("python environment directory must be absolute: %#q", dir)
	}

	python := filepath.Join(dir, "bin", "python3")
	for _, path := range []string{python, filepath.Join(dir, "pyvenv.cfg")} {
		info, err := ctx.FS().Stat(path)
		if err != nil {
			return "", fmt.Errorf("caller-prepared Python environment %#q is missing or incomplete:\n%w", dir, err)
		}

		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("caller-prepared Python environment requires a regular file %#q", path)
		}
	}

	return python, nil
}

// Install installs explicitly requested packages into an isolated environment.
func Install(ctx opctx.Ctx, python, dir string, args ...string) error {
	commandArgs := append([]string{"-m", "pip", "install", "--quiet"}, args...)
	if err := externalcmd.Run(ctx, dir, python, commandArgs...); err != nil {
		return fmt.Errorf("install Python dependencies:\n%w", err)
	}

	return nil
}
