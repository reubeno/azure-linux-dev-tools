// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/binalyze/ctxio"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
)

// Snapshot copies a file into a fresh caller-owned directory and records its digest.
// It never hardlinks mutable input files. OCI identities already identify immutable
// content and need no file copy. Callers remove successful snapshots after execution.
func Snapshot(ctx opctx.Ctx, input Artifact, directory string) (result Artifact, err error) {
	if err := input.ValidateLocation(); err != nil {
		return result, err
	}

	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("snapshot cancelled:\n%w", err)
	}

	if input.OCIImageID != "" {
		return input, nil
	}

	if !filepath.IsAbs(directory) {
		return result, errors.New("snapshot directory must be absolute")
	}

	if ctx.DryRun() {
		return input, nil
	}

	if err := fileutils.MkdirAll(ctx.FS(), filepath.Dir(directory)); err != nil {
		return result, fmt.Errorf("create snapshot parent:\n%w", err)
	}

	if err := ctx.FS().Mkdir(directory, fileperms.PrivateDir); err != nil {
		return result, fmt.Errorf("create fresh artifact snapshot %#q:\n%w", directory, err)
	}

	defer func() {
		if err != nil {
			if cleanupErr := ctx.FS().RemoveAll(directory); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("remove partial snapshot:\n%w", cleanupErr))
			}
		}
	}()

	target := filepath.Join(directory, filepath.Base(input.Path))

	digest, err := snapshotFile(ctx, input, target)
	if err != nil {
		return result, err
	}

	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("snapshot cancelled:\n%w", err)
	}

	if err := ctx.FS().Chmod(target, fileperms.PrivateReadOnlyFile); err != nil {
		return result, fmt.Errorf("protect artifact snapshot:\n%w", err)
	}

	result = input

	result.Path, result.SHA256 = target, digest
	if result.OriginalPath == "" {
		result.OriginalPath = input.Path
	}

	return result, nil
}

func snapshotFile(ctx opctx.Ctx, input Artifact, target string) (digest string, err error) {
	source, err := ctx.FS().Open(input.Path)
	if err != nil {
		return "", fmt.Errorf("open snapshot input %#q:\n%w", input.Path, err)
	}

	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close snapshot input:\n%w", closeErr))
		}
	}()

	before, err := source.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect snapshot input:\n%w", err)
	}

	if !before.Mode().IsRegular() {
		return "", fmt.Errorf("snapshot input %#q is not a regular file", input.Path)
	}

	beforeSize, beforeTime := before.Size(), before.ModTime()
	if input.OriginalPath == "" && !input.ModifiedAt.IsZero() && !input.ModifiedAt.Equal(before.ModTime()) {
		return "", fmt.Errorf("input %#q changed since selection; retry after the build completes", input.Path)
	}

	output, err := ctx.FS().OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileperms.PrivateFile)
	if err != nil {
		return "", fmt.Errorf("create artifact snapshot:\n%w", err)
	}

	hash := sha256.New()

	_, copyErr := io.Copy(ctxio.NewWriter(ctx, io.MultiWriter(output, hash), func(int64) {}), source)
	if err := errors.Join(copyErr, output.Close()); err != nil {
		return "", fmt.Errorf("copy artifact snapshot:\n%w", err)
	}

	after, err := source.Stat()
	if err != nil {
		return "", fmt.Errorf("recheck snapshot input:\n%w", err)
	}

	if beforeSize != after.Size() || !beforeTime.Equal(after.ModTime()) {
		return "", fmt.Errorf("input %#q changed during snapshot; retry after the build completes", input.Path)
	}

	digest = hex.EncodeToString(hash.Sum(nil))
	if input.SHA256 != "" && !strings.EqualFold(input.SHA256, digest) {
		return "", fmt.Errorf("snapshot SHA-256 mismatch for %#q: expected %s, got %s", input.Path, input.SHA256, digest)
	}

	return digest, nil
}
