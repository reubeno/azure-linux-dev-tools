// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package testsource materializes framework and test repositories without owning caller checkouts.
package testsource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/externalcmd"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/prereqs"
)

// Materialize copies a local tree or clones a pinned source into a new, private destination.
// The caller owns the destination after success and must remove it when the run finishes.
// Existing destinations are never reused or removed.
func Materialize(ctx opctx.Ctx, source *projectconfig.GitSourceConfig,
	local, destination string,
) (dir string, err error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("materialize test source cancelled:\n%w", err)
	}

	if !filepath.IsAbs(destination) {
		return "", fmt.Errorf("source destination must be absolute: %#q", destination)
	}

	if err := validateSource(ctx, source, local, destination); err != nil {
		return "", err
	}

	if ctx.DryRun() {
		return destination, nil
	}

	if err := fileutils.MkdirAll(ctx.FS(), filepath.Dir(destination)); err != nil {
		return "", fmt.Errorf("create source destination parent:\n%w", err)
	}

	if err := ctx.FS().Mkdir(destination, fileperms.PrivateDir); err != nil {
		return "", fmt.Errorf("create fresh source destination %#q:\n%w", destination, err)
	}

	defer func() {
		if err != nil {
			err = errors.Join(err, ctx.FS().RemoveAll(destination))
		}
	}()

	if local != "" {
		if err := copyLocal(ctx, local, destination); err != nil {
			return "", fmt.Errorf("copy local test source:\n%w", err)
		}
	} else if err := clone(ctx, source, destination); err != nil {
		return "", err
	}

	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("materialize test source cancelled:\n%w", err)
	}

	return destination, nil
}

func validateSource(ctx opctx.Ctx, source *projectconfig.GitSourceConfig, local, destination string) error {
	if local != "" {
		if _, err := localDirectory(ctx, local); err != nil {
			return err
		}

		return CheckDestination(local, destination)
	}

	if source == nil {
		return errors.New("a pinned source or local source directory is required")
	}

	if err := source.Validate("test source"); err != nil {
		return fmt.Errorf("validate test source:\n%w", err)
	}

	return nil
}

func copyLocal(ctx opctx.Ctx, local, destination string) error {
	info, err := ctx.FS().Stat(filepath.Join(local, ".git"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check local Git metadata:\n%w", err)
	}

	linked := err == nil && !info.IsDir()
	if linked {
		// A worktree's '.git' file points back into caller-owned metadata.
		// Clone independent metadata, then overlay the exact local files, including
		// uncommitted changes. A mixed reset populates only the private index.
		if err := prereqs.RequireExecutable(ctx, "git", nil); err != nil {
			return fmt.Errorf("copy linked Git worktree:\n%w", err)
		}

		args := []string{"clone", "--no-hardlinks", "--no-checkout", local, destination}
		if err := externalcmd.Run(ctx, "", "git", args...); err != nil {
			return fmt.Errorf("clone linked worktree metadata:\n%w", err)
		}

		if err := externalcmd.Run(ctx, "", "git", "-C", destination, "reset", "--mixed", "HEAD"); err != nil {
			return fmt.Errorf("populate private worktree index:\n%w", err)
		}
	}

	options := fileutils.CopyDirOptions{
		CopyFileOptions: fileutils.CopyFileOptions{PreserveFileMode: true},
		FileFilter: func(_ opctx.FS, path string) (bool, error) {
			return !linked || path != filepath.Join(destination, ".git"), ctx.Err()
		},
	}

	if err := fileutils.CopyDirRecursive(ctx, ctx.FS(), local, destination, options); err != nil {
		return fmt.Errorf("copy source snapshot:\n%w", err)
	}

	return nil
}

// CheckDestination rejects output locations that would modify or recursively copy the source.
func CheckDestination(local, destination string) error {
	if local == "" {
		return nil
	}

	if !filepath.IsAbs(local) || !filepath.IsAbs(destination) {
		return errors.New("test source and destination directories must be absolute")
	}

	for _, pair := range [][2]string{{local, destination}, {destination, local}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("test source and destination directories must not overlap")
		}
	}

	return nil
}

func clone(ctx opctx.Ctx, source *projectconfig.GitSourceConfig, dir string) error {
	if err := prereqs.RequireExecutable(ctx, "git", nil); err != nil {
		return fmt.Errorf("test source prerequisite:\n%w", err)
	}

	if err := externalcmd.Run(ctx, "", "git", "clone", "--no-hardlinks", source.GitURL, dir); err != nil {
		return fmt.Errorf("clone test source:\n%w", err)
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("clone test source cancelled:\n%w", err)
	}

	if err := externalcmd.Run(ctx, "", "git", "-C", dir, "checkout", source.Ref); err != nil {
		return fmt.Errorf("checkout pinned test source:\n%w", err)
	}

	return nil
}

func localDirectory(ctx opctx.Ctx, local string) (string, error) {
	if !filepath.IsAbs(local) {
		return "", fmt.Errorf("source directory must be absolute: %#q", local)
	}

	exists, err := fileutils.DirExists(ctx.FS(), local)
	if err != nil {
		return "", fmt.Errorf("check local test source:\n%w", err)
	}

	if !exists {
		return "", fmt.Errorf("source directory %#q does not exist", local)
	}

	return local, nil
}
