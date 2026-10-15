// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package externalcmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
)

// Run executes a tool through the operation context, retaining stderr on failure.
func Run(ctx opctx.Ctx, dir, program string, args ...string) error {
	return runWithGrace(ctx, dir, program, 0, args...)
}

// RunInterruptible gives tools time to release owned resources on cancellation.
func RunInterruptible(ctx opctx.Ctx, dir, program string, grace time.Duration, args ...string) error {
	return runWithGrace(ctx, dir, program, grace, args...)
}

func runWithGrace(ctx opctx.Ctx, dir, program string, grace time.Duration, args ...string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("command cancelled:\n%w", err)
	}

	if ctx.DryRun() {
		return nil
	}

	raw := exec.CommandContext(ctx, program, args...)
	raw.Dir = dir

	raw.Stdout = os.Stdout
	if grace > 0 {
		raw.Cancel = func() error {
			if raw.Process == nil {
				return os.ErrProcessDone
			}

			return raw.Process.Signal(os.Interrupt)
		}
		raw.WaitDelay = grace
	}

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
