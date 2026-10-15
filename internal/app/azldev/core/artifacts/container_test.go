// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package artifacts_test

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/artifacts"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishedContainerRetainsPullIdentity(t *testing.T) {
	for _, arch := range []struct{ azldev, oci string }{{"x86_64", "amd64"}, {"aarch64", "arm64"}} {
		t.Run(arch.azldev, func(t *testing.T) {
			env := testutils.NewTestEnv(t)
			env.CmdFactory.RegisterCommandInSearchPath("podman")
			env.CmdFactory.RunHandler = func(*exec.Cmd) error {
				t.Fatal("pull output must be captured, never followed by a mutable tag lookup")

				return nil
			}
			calls := 0
			env.CmdFactory.RunAndGetOutputHandler = func(cmd *exec.Cmd) (string, error) {
				calls++

				assert.Equal(t, []string{"podman", "pull", "--quiet", "--arch", arch.oci, "example.test/base:lkg"}, cmd.Args)

				return strings.Repeat("a", 64) + "\n", nil
			}
			resolver := artifacts.Resolver{Env: env.Env}
			distro := projectconfig.DistroVersionDefinition{Images: map[string]projectconfig.PublishedImage{
				"base": {Architectures: map[string][]projectconfig.PublishedImageArtifact{
					arch.azldev: {{Format: "oci", URI: "docker://example.test/base:lkg"}},
				}},
			}}
			got, err := resolver.Image("base", arch.azldev, []string{"oci"}, artifacts.Selection{Source: "lkg"}, distro)
			require.NoError(t, err)
			assert.Equal(t, 1, calls)
			assert.Equal(t, "sha256:"+strings.Repeat("a", 64), got.Path)
			assert.Equal(t, "docker://example.test/base:lkg", got.URI)
			assert.Equal(t, arch.azldev, got.Arch)
		})
	}
}

func TestPublishedContainerRejectsBadPull(t *testing.T) {
	pullErr := errors.New("registry unavailable")
	for _, test := range []struct {
		name, output, want string
		err                error
	}{
		{"empty identity", "", "single SHA-256", nil},
		{"tag instead of identity", "base:lkg", "single SHA-256", nil},
		{"multiple identities", strings.Repeat("a", 64) + "\n" + strings.Repeat("b", 64), "single SHA-256", nil},
		{"failed pull", strings.Repeat("a", 64), "registry diagnostic", pullErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := testutils.NewTestEnv(t)
			env.CmdFactory.RegisterCommandInSearchPath("podman")
			env.CmdFactory.RunAndGetOutputHandler = func(cmd *exec.Cmd) (string, error) {
				_, err := cmd.Stderr.Write([]byte("registry diagnostic"))
				require.NoError(t, err)

				return test.output, test.err
			}
			resolver := artifacts.Resolver{Env: env.Env}
			distro := projectconfig.DistroVersionDefinition{Images: map[string]projectconfig.PublishedImage{
				"base": {Architectures: map[string][]projectconfig.PublishedImageArtifact{
					"x86_64": {{Format: "oci", URI: "docker://example.test/base:lkg"}},
				}},
			}}
			_, err := resolver.Image("base", "x86_64", []string{"oci"}, artifacts.Selection{Source: "lkg"}, distro)
			require.ErrorContains(t, err, test.want)

			if test.err != nil {
				require.ErrorIs(t, err, test.err)
			}
		})
	}
}
