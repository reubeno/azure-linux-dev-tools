// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tmt_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/testctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/tmt"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTMTUsesImmutableGitConstraintAndRetainsIt(t *testing.T) {
	var constraint string
	for _, line := range strings.Split(string(pinnedRequirements), "\n") {
		if strings.HasPrefix(line, "tmt @ ") {
			constraint = line
		}
	}

	require.Regexp(t, `^tmt @ git\+https://github\.com/teemtee/tmt\.git@[0-9a-f]{40}$`, constraint)
	ctx := newRunContext(t)
	runner, err := tmt.New("test", nil)
	require.NoError(t, err)
	_, err = runner.Run(ctx, requestFor(testframework.Host))
	require.NoError(t, err)
	recorded, err := fileutils.ReadFile(ctx.FS(), workDir+"/tmt-requirements.txt")
	require.NoError(t, err)
	assert.Equal(t, pinnedRequirements, recorded)
}

func TestGitPinnedRuntimeFailsClearlyWithoutGit(t *testing.T) {
	ctx := testctx.NewCtx()
	for _, program := range []string{"sudo", "python3"} {
		ctx.CmdFactory.RegisterCommandInSearchPath(program)
	}

	writeFile(t, ctx, "/etc/os-release", "ID=azurelinux\nVERSION_ID=4.0\n")
	writeFile(t, ctx, sourceDir+"/.fmf/version", "1\n")
	ctx.CmdFactory.RunHandler = func(*exec.Cmd) error {
		t.Fatal("missing git must fail before environment installation")

		return nil
	}
	runner, err := tmt.New("test", nil)
	require.NoError(t, err)
	_, err = runner.Run(ctx, requestFor(testframework.Host))
	require.ErrorContains(t, err, "git-pinned TMT runtime requires git")
}
