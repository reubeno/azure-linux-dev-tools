// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testexecution

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/microsoft/azure-linux-dev-tools/internal/artifact"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
)

// Each invocation freezes its selected files once. Tests share read-only snapshots,
// not mutable build output or another invocation's writable runtime.
func snapshotInputs(ctx opctx.Ctx, inputs []testInputs) (
	snapshots []testInputs, cleanup func() error, err error,
) {
	root, err := fileutils.MkdirTempInTempDir(ctx.FS(), "azldev-test-inputs-")
	if err != nil {
		return nil, nil, fmt.Errorf("create private test inputs:\n%w", err)
	}

	cleanup = func() error {
		if err := ctx.FS().RemoveAll(root); err != nil {
			return fmt.Errorf("remove private test inputs:\n%w", err)
		}

		return nil
	}
	seen := make(map[artifact.Artifact]artifact.Artifact)
	copyInput := func(input artifact.Artifact) (artifact.Artifact, error) {
		if existing, ok := seen[input]; ok {
			return existing, nil
		}

		result, err := artifact.Snapshot(ctx, input, filepath.Join(root, strconv.Itoa(len(seen))))
		if err != nil {
			return result, fmt.Errorf("snapshot materialized test input:\n%w", err)
		}

		seen[input] = result

		return result, nil
	}

	snapshots = make([]testInputs, len(inputs))
	for index, input := range inputs {
		if input.Image != nil {
			image, copyErr := copyInput(*input.Image)
			if copyErr != nil {
				return nil, nil, errors.Join(copyErr, cleanup())
			}

			snapshots[index].Image = &image
		}

		for _, candidate := range input.Packages {
			pkg, copyErr := copyInput(candidate)
			if copyErr != nil {
				return nil, nil, errors.Join(copyErr, cleanup())
			}

			snapshots[index].Packages = append(snapshots[index].Packages, pkg)
		}
	}

	return snapshots, cleanup, nil
}
