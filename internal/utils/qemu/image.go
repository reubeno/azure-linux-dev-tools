// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package qemu

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/externalcmd"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/prereqs"
)

// DiskFormats names disk formats accepted by the local VM test adapters.
func DiskFormats() []string {
	return []string{"qcow2", "raw", "vhd", "vhdx"}
}

// EnsureQcow2 detects actual disk content and converts only when necessary.
// The source is never modified; destination must be owned by the current run.
func EnsureQcow2(ctx opctx.Ctx, source, destination string) (string, error) {
	if !filepath.IsAbs(source) || !filepath.IsAbs(destination) || filepath.Clean(source) == filepath.Clean(destination) {
		return "", errors.New("image conversion requires distinct absolute source and destination paths")
	}

	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("image conversion cancelled:\n%w", err)
	}

	if ctx.DryRun() {
		return destination, nil
	}

	format, err := detectDiskFormat(ctx, source)
	if err != nil {
		return "", err
	}

	if format == "qcow2" {
		return source, nil
	}

	if !slices.Contains([]string{"raw", "vpc", "vhdx"}, format) {
		return "", fmt.Errorf("unsupported actual disk format %#q", format)
	}

	exists, err := fileutils.Exists(ctx.FS(), destination)
	if err != nil {
		return "", fmt.Errorf("check converted image destination:\n%w", err)
	}

	if exists {
		return "", fmt.Errorf("converted image destination already exists: %#q", destination)
	}

	if err := fileutils.MkdirAll(ctx.FS(), filepath.Dir(destination)); err != nil {
		return "", fmt.Errorf("create image conversion directory:\n%w", err)
	}

	if err := externalcmd.Run(ctx, "", "qemu-img", "convert", "-f", format,
		"-O", "qcow2", source, destination); err != nil {
		return "", fmt.Errorf("convert disk image:\n%w", err)
	}

	return destination, nil
}

func detectDiskFormat(ctx opctx.Ctx, source string) (string, error) {
	if err := prereqs.RequireExecutable(ctx, "qemu-img", nil); err != nil {
		return "", fmt.Errorf("disk image inspection requires qemu-img:\n%w", err)
	}

	raw := exec.CommandContext(ctx, "qemu-img", "info", "--output=json", source)

	var stderr strings.Builder

	raw.Stderr = &stderr

	cmd, err := ctx.Command(raw)
	if err != nil {
		return "", fmt.Errorf("create disk format query:\n%w", err)
	}

	output, err := cmd.RunAndGetOutput(ctx)
	if err != nil {
		return "", fmt.Errorf("detect disk format (%s):\n%w", stderr.String(), err)
	}

	var info struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal([]byte(output), &info); err != nil {
		return "", fmt.Errorf("parse disk format:\n%w", err)
	}

	return info.Format, nil
}
