// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package pytestfixtures embeds the Python pytest fixtures for image validation
// and provides a function to extract them to a temporary directory.
package pytestfixtures

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
)

//go:embed azldev_check/*.py
var content embed.FS

// ExtractTo copies the embedded Python fixtures to the specified destination directory.
// The azldev_check package will be placed as a subdirectory inside destPath, so that
// destPath can be added to PYTHONPATH for import resolution.
func ExtractTo(targetFS opctx.FS, destPath string) error {
	err := fs.WalkDir(content, "azldev_check", func(path string, dirEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		destFilePath := filepath.Join(destPath, path)

		if dirEntry.IsDir() {
			return fileutils.MkdirAll(targetFS, destFilePath)
		}

		data, readErr := content.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("failed to read embedded file %#q:\n%w", path, readErr)
		}

		return fileutils.WriteFile(targetFS, destFilePath, data, fileperms.PublicFile)
	})
	if err != nil {
		return fmt.Errorf("failed to extract pytest fixtures to %#q:\n%w", destPath, err)
	}

	return nil
}
