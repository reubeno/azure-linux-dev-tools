// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testframework

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
)

// Command runs an external tool through the operation context, retaining stderr on failure.
func Command(ctx opctx.Ctx, dir, program string, args ...string) error {
	raw := exec.CommandContext(ctx, program, args...)
	raw.Dir = dir
	raw.Stdout = os.Stdout

	var stderr bytes.Buffer
	raw.Stderr = io.MultiWriter(os.Stderr, &stderr)

	cmd, err := ctx.Command(raw)
	if err != nil {
		return fmt.Errorf("create command %#q:\n%w", program, err)
	}
	if err := cmd.Run(ctx); err != nil {
		return fmt.Errorf("run %#q (%s):\n%w", program, strings.TrimSpace(stderr.String()), err)
	}

	return nil
}
