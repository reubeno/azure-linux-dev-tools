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

// Freeze records installed distributions without requiring pip or changing the environment.
func Freeze(ctx opctx.Ctx, python, destination string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("runtime dependency query cancelled:\n%w", err)
	}

	if ctx.DryRun() {
		return nil
	}

	const metadataScript = "import importlib.metadata as m, json, sys; " +
		"print('# Python ' + sys.version.replace('\\n', ' ')); " +
		"print('\\n'.join(sorted(d.metadata['Name'] + '==' + d.version for d in m.distributions()))); " +
		"print('\\n'.join('# source ' + d.metadata['Name'] + ': ' + json.dumps(json.loads(raw), sort_keys=True) " +
		"for d in m.distributions() if (raw := d.read_text('direct_url.json'))))"

	raw := exec.CommandContext(ctx, python, "-I", "-B", "-c", metadataScript)

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
