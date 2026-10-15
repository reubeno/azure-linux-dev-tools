// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package qemu_test

import (
	"os/exec"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/testctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/qemu"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImageConversionUsesContentNotExtension(t *testing.T) {
	for _, format := range []string{"qcow2", "raw", "vpc", "vhdx"} {
		t.Run(format, func(t *testing.T) {
			ctx := testctx.NewCtx()
			ctx.CmdFactory.RegisterCommandInSearchPath("qemu-img")
			ctx.CmdFactory.RunAndGetOutputHandler = func(cmd *exec.Cmd) (string, error) {
				assert.Equal(t, []string{"qemu-img", "info", "--output=json", "/image.wrong"}, cmd.Args)

				return `{"format":"` + format + `"}`, nil
			}

			var commands [][]string

			ctx.CmdFactory.RunHandler = func(cmd *exec.Cmd) error {
				commands = append(commands, cmd.Args)

				return nil
			}
			path, err := qemu.EnsureQcow2(ctx, "/image.wrong", "/work/image.qcow2")
			require.NoError(t, err)

			if format == "qcow2" {
				assert.Equal(t, "/image.wrong", path)
				assert.Empty(t, commands)
			} else {
				assert.Equal(t, "/work/image.qcow2", path)
				assert.Equal(t, [][]string{{
					"qemu-img", "convert", "-f", format, "-O", "qcow2",
					"/image.wrong", "/work/image.qcow2",
				}}, commands)
			}
		})
	}
}
