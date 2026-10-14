// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package testsource materializes framework and test repositories without owning caller checkouts.
package testsource

import (
	"fmt"
	"path/filepath"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/git"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/prereqs"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/pythonenv"
)

// Materialize resolves a local directory or a managed checkout keyed by URL and full commit.
// The returned checkout is read-only input; per-run generated files belong in the run directory.
func Materialize(ctx opctx.Ctx, source *projectconfig.GitSourceConfig, local, cacheDir string) (string, error) {
	if local != "" {
		if !filepath.IsAbs(local) {
			return "", fmt.Errorf("source directory must be absolute: %#q", local)
		}
		exists, err := fileutils.DirExists(ctx.FS(), local)
		if err != nil {
			return "", err
		}
		if !exists {
			return "", fmt.Errorf("source directory %#q does not exist", local)
		}

		return local, nil
	}
	if source == nil {
		return "", fmt.Errorf("a pinned source or local source directory is required")
	}
	if err := source.Validate("test source"); err != nil {
		return "", err
	}
	if !filepath.IsAbs(cacheDir) {
		return "", fmt.Errorf("source cache directory must be absolute: %#q", cacheDir)
	}
	dir := filepath.Join(cacheDir, "sources", pythonenv.Key(source.GitURL, source.Ref))
	if ctx.DryRun() {
		return dir, nil
	}
	if err := prereqs.RequireExecutable(ctx, "git", nil); err != nil {
		return "", err
	}
	provider, err := git.NewGitProviderImpl(ctx, ctx)
	if err != nil {
		return "", err
	}
	exists, err := fileutils.DirExists(ctx.FS(), dir)
	if err != nil {
		return "", err
	}
	if !exists {
		if err := fileutils.MkdirAll(ctx.FS(), filepath.Dir(dir)); err != nil {
			return "", err
		}
		if err := provider.Clone(ctx, source.GitURL, dir); err != nil {
			return "", err
		}
	}
	if err := provider.Checkout(ctx, dir, source.Ref); err != nil {
		return "", fmt.Errorf("checkout pinned test source:\n%w", err)
	}

	return dir, nil
}
