// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package pythonenv manages isolated Python installations for external tools.
package pythonenv

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/prereqs"
)

// Key returns a stable cache key for a tool and its installation inputs.
func Key(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))

	return hex.EncodeToString(sum[:])
}

// Ensure creates a virtual environment when needed and returns its Python executable.
func Ensure(ctx opctx.Ctx, dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("Python environment directory must be absolute: %#q", dir)
	}
	python := filepath.Join(dir, "bin", "python3")
	if ctx.DryRun() {
		return python, nil
	}
	if err := prereqs.RequireExecutable(ctx, "python3", nil); err != nil {
		return "", err
	}
	exists, err := fileutils.Exists(ctx.FS(), python)
	if err != nil {
		return "", fmt.Errorf("check Python environment:\n%w", err)
	}
	if !exists {
		if err := fileutils.MkdirAll(ctx.FS(), filepath.Dir(dir)); err != nil {
			return "", err
		}
		if err := testframework.Command(ctx, "", "python3", "-m", "venv", dir); err != nil {
			return "", err
		}
	}

	return python, nil
}

// Install installs explicitly requested packages into an isolated environment.
func Install(ctx opctx.Ctx, python, dir string, args ...string) error {
	return testframework.Command(ctx, dir, python, append([]string{"-m", "pip", "install", "--quiet"}, args...)...)
}
