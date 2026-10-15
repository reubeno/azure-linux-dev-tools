// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pythonenv

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
)

// Freeze records the actual installed dependencies before a private environment is removed.
func Freeze(ctx opctx.Ctx, python, destination string) error {
	if ctx.DryRun() {
		return nil
	}

	raw := exec.CommandContext(ctx, python, "-m", "pip", "freeze")

	var stderr strings.Builder

	raw.Stderr = &stderr

	cmd, err := ctx.Command(raw)
	if err != nil {
		return fmt.Errorf("create runtime dependency query:\n%w", err)
	}

	output, err := cmd.RunAndGetOutput(ctx)
	if err != nil {
		return fmt.Errorf("query runtime dependencies (%s):\n%w", stderr.String(), err)
	}

	if err := fileutils.MkdirAll(ctx.FS(), filepath.Dir(destination)); err != nil {
		return fmt.Errorf("create runtime report directory:\n%w", err)
	}

	if err := fileutils.WriteFile(ctx.FS(), destination, []byte(output+"\n"), fileperms.PrivateFile); err != nil {
		return fmt.Errorf("write runtime dependency report:\n%w", err)
	}

	return nil
}
