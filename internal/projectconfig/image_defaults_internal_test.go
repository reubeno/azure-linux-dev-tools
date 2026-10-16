// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package projectconfig

import (
	"strings"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/testctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImageDefaultsSurviveLoadingAndPathResolution(t *testing.T) {
	for _, commands := range []string{`["echo configured"]`, `[]`} {
		t.Run(commands, func(t *testing.T) {
			ctx := testctx.NewCtx()
			content := []string{
				`includes = ["images.toml"]`,
				`[distros.azl.versions."4".images.base.architectures]`,
				`x86_64 = [{ format = "qcow2", uri = "https://example.test/base.qcow2" }]`,
				`[distros.azl.versions."4".images.base.test-defaults.tmt]`,
				`cloud-init-runcmds = ["echo published"]`,
			}
			require.NoError(t, fileutils.WriteFile(ctx.FS(), "/project/azldev.toml",
				[]byte(strings.Join(content, "\n")+"\n"), fileperms.PrivateFile))
			require.NoError(t, fileutils.WriteFile(ctx.FS(), "/project/images.toml",
				[]byte("[images.base.test-defaults.tmt]\ncloud-init-runcmds = "+commands+"\n"), fileperms.PrivateFile))
			cfg, err := loadAndResolveProjectConfig(ctx.FS(), loadOptions{}, "/project/azldev.toml")
			require.NoError(t, err)

			defaults := cfg.Images["base"].TestDefaults
			require.NotNil(t, defaults)
			require.NotNil(t, defaults.TMT.CloudInitRuncmds)

			if commands == `[]` {
				assert.Empty(t, *defaults.TMT.CloudInitRuncmds)
			} else {
				assert.Equal(t, []string{"echo configured"}, *defaults.TMT.CloudInitRuncmds)
			}

			assert.Equal(t, []string{"echo published"},
				*cfg.Distros["azl"].Versions["4"].Images["base"].TestDefaults.TMT.CloudInitRuncmds)
		})
	}
}

func TestImageDefaultsMergeReplacesListsWithoutAliasing(t *testing.T) {
	for _, values := range [][]string{nil, {}, {"echo replacement"}} {
		t.Run(strings.Join(values, ","), func(t *testing.T) {
			original := []string{"echo original"}
			image := ImageConfig{TestDefaults: &ImageTestDefaults{
				TMT: TMTImageTestDefaults{CloudInitRuncmds: &original},
			}}

			other := ImageConfig{TestDefaults: &ImageTestDefaults{}}
			if values != nil {
				other.TestDefaults.TMT.CloudInitRuncmds = &values
			}

			require.NoError(t, image.MergeUpdatesFrom(&other))

			if values == nil {
				assert.Equal(t, original, *image.TestDefaults.TMT.CloudInitRuncmds)
			} else {
				assert.Equal(t, values, *image.TestDefaults.TMT.CloudInitRuncmds)

				if len(values) > 0 {
					values[0] = "caller edit"

					assert.Equal(t, []string{"echo replacement"}, *image.TestDefaults.TMT.CloudInitRuncmds)
				}
			}

			resolved := image.WithAbsolutePaths("/other")
			require.NotNil(t, resolved.TestDefaults)
			*resolved.TestDefaults.TMT.CloudInitRuncmds = []string{"copy edit"}
			assert.NotEqual(t, *resolved.TestDefaults.TMT.CloudInitRuncmds, *image.TestDefaults.TMT.CloudInitRuncmds)
		})
	}

	image := ImageConfig{}
	values := []string{"echo new"}
	require.NoError(t, image.MergeUpdatesFrom(&ImageConfig{TestDefaults: &ImageTestDefaults{
		TMT: TMTImageTestDefaults{CloudInitRuncmds: &values},
	}}))
	values[0] = "caller edit"

	assert.Equal(t, []string{"echo new"}, *image.TestDefaults.TMT.CloudInitRuncmds)
}
