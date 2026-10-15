// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package artifact

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
)

// RecordPath names the build manifest without interpreting artifact acquisition policy.
func RecordPath(output, kind, name, arch string) string {
	return filepath.Join(output, "build-records", kind, name, arch+".json")
}

// RecordBuild atomically publishes a complete successful-build manifest.
func RecordBuild(ctx opctx.Ctx, output string, record BuildRecord) error {
	if ctx.DryRun() {
		return nil
	}

	for _, name := range []string{record.Kind, record.Name, record.Arch} {
		if err := fileutils.ValidateFilename(name); err != nil {
			return fmt.Errorf("invalid build record identity:\n%w", err)
		}
	}

	if len(record.Paths) == 0 {
		return errors.New("cannot record a build with no outputs")
	}

	for _, path := range record.Paths {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("build output path must be absolute: %#q", path)
		}
	}

	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode build record:\n%w", err)
	}

	dest := RecordPath(output, record.Kind, record.Name, record.Arch)
	if err := fileutils.MkdirAll(ctx.FS(), filepath.Dir(dest)); err != nil {
		return fmt.Errorf("create build record directory:\n%w", err)
	}

	tempDir, err := fileutils.MkdirTemp(ctx.FS(), filepath.Dir(dest), ".record-*")
	if err != nil {
		return fmt.Errorf("create temporary build record:\n%w", err)
	}

	defer func() {
		if err := ctx.FS().RemoveAll(tempDir); err != nil {
			slog.Warn("Failed to remove temporary build record", "path", tempDir, "error", err)
		}
	}()

	temp := filepath.Join(tempDir, "record.json")
	if err := fileutils.WriteFile(ctx.FS(), temp, data, fileperms.PrivateFile); err != nil {
		return fmt.Errorf("write build record:\n%w", err)
	}

	if err := ctx.FS().Rename(temp, dest); err != nil {
		return fmt.Errorf("publish build record:\n%w", err)
	}

	return nil
}
