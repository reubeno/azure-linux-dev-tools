// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package qemu_test

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/testctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/qemu"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImageConversionRejectsInvalidPathsBeforeCommands(t *testing.T) {
	for _, paths := range [][2]string{
		{"image.raw", "/work/image.qcow2"},
		{"/image.raw", "image.qcow2"},
		{"/image.raw", "/image.raw"},
		{"/images/../image.raw", "/image.raw"},
	} {
		ctx := testctx.NewCtx()
		ctx.CmdFactory.RunAndGetOutputHandler = func(*exec.Cmd) (string, error) {
			t.Fatal("invalid paths must fail before inspection")

			return "", nil
		}
		_, err := qemu.EnsureQcow2(ctx, paths[0], paths[1])
		require.ErrorContains(t, err, "distinct absolute")
	}
}

func TestImageConversionRejectsMalformedOrUnsupportedContent(t *testing.T) {
	for _, output := range []string{"{", `{}`, `{"format":"vmdk"}`} {
		ctx := testctx.NewCtx()
		ctx.CmdFactory.RegisterCommandInSearchPath("qemu-img")
		ctx.CmdFactory.RunAndGetOutputHandler = func(*exec.Cmd) (string, error) { return output, nil }
		ctx.CmdFactory.RunHandler = func(*exec.Cmd) error {
			t.Fatal("invalid format must not start conversion")

			return nil
		}
		_, err := qemu.EnsureQcow2(ctx, "/image.raw", "/work/image.qcow2")
		require.Error(t, err)
	}
}

func TestImageConversionPreservesExistingDestination(t *testing.T) {
	ctx := testctx.NewCtx()
	ctx.CmdFactory.RegisterCommandInSearchPath("qemu-img")
	require.NoError(t, fileutils.MkdirAll(ctx.FS(), "/work"))
	require.NoError(t, fileutils.WriteFile(ctx.FS(), "/work/image.qcow2", []byte("keep"), fileperms.PrivateFile))
	ctx.CmdFactory.RunAndGetOutputHandler = func(*exec.Cmd) (string, error) { return `{"format":"raw"}`, nil }
	ctx.CmdFactory.RunHandler = func(*exec.Cmd) error {
		t.Fatal("existing output must not be overwritten")

		return nil
	}
	_, err := qemu.EnsureQcow2(ctx, "/image.raw", "/work/image.qcow2")
	require.ErrorContains(t, err, "already exists")
	data, err := fileutils.ReadFile(ctx.FS(), "/work/image.qcow2")
	require.NoError(t, err)
	assert.Equal(t, "keep", string(data))
}

func TestImageConversionPreservesCommandErrors(t *testing.T) {
	for _, inspection := range []bool{false, true} {
		ctx := testctx.NewCtx()
		ctx.CmdFactory.RegisterCommandInSearchPath("qemu-img")

		failure := errors.New("native image error")
		ctx.CmdFactory.RunAndGetOutputHandler = func(cmd *exec.Cmd) (string, error) {
			if inspection {
				_, err := cmd.Stderr.Write([]byte("inspection diagnostic"))
				require.NoError(t, err)

				return "", failure
			}

			return `{"format":"vhdx"}`, nil
		}
		ctx.CmdFactory.RunHandler = func(*exec.Cmd) error { return failure }
		_, err := qemu.EnsureQcow2(ctx, "/image.raw", "/work/image.qcow2")
		require.ErrorIs(t, err, failure)

		if inspection {
			require.ErrorContains(t, err, "inspection diagnostic")
		}
	}
}

func TestImageConversionDryRunAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		lifetime, cancel := context.WithCancel(t.Context())
		defer cancel()

		if cancelled {
			cancel()
		}

		ctx := testctx.NewCtx()
		ctx.DryRunValue = !cancelled

		ctx.CmdFactory.RunAndGetOutputHandler = func(*exec.Cmd) (string, error) {
			t.Fatal("dry or cancelled conversion must not inspect disk contents")

			return "", nil
		}

		path, err := qemu.EnsureQcow2(opctx.WithContext(ctx, lifetime), "/image.raw", "/work/image.qcow2")
		if cancelled {
			require.ErrorIs(t, err, context.Canceled)
		} else {
			require.NoError(t, err)
			assert.Equal(t, "/work/image.qcow2", path)
		}

		entries, err := fileutils.ReadDir(ctx.FS(), "/")
		require.NoError(t, err)
		assert.Empty(t, entries)
	}
}

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
