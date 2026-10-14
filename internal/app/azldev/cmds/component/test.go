// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package component

import (
	"errors"
	"fmt"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/artifacts"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/components"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/imagebuilder"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testexecution"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/spf13/cobra"
)

func testOnAppInit(_ *azldev.App, parentCmd *cobra.Command) {
	parentCmd.AddCommand(NewComponentTestCmd())
}

// NewComponentTestCmd constructs the framework-independent component test command.
func NewComponentTestCmd() *cobra.Command {
	options := &testexecution.Options{}

	var (
		fromSpec  bool
		provision string
	)

	cmd := &cobra.Command{
		Use:   "test COMPONENT",
		Short: "Run tests against a component's build artifacts",
		Long: `Run associated LISA, TMT, or pytest tests against a component.

By default, reuse the component's local binary build output, or build it if
missing. Use --rebuild to force a new build, --artifact-source lkg to test a
published package set, or --rpm to supply explicit candidate packages. All
binary subpackages are installed by live-test frameworks unless --rpm narrows
the set. Conflicting subpackages currently require an explicit selection.

Supporting VM/container images default to named published LKG images in the
selected distro version. Override independently with --base-image,
--base-image-path, --base-image-source local, or --rebuild-base-image.
Local image builds require a project-local image recipe.

TMT supports --execution vm, container, ssh, and host. LISA supports vm;
pytest runs on the host and receives the artifact input manifest. Unsupported
combinations fail before execution. Host and SSH tests may modify the target;
only use disposable or explicitly designated test systems.

Use --test to select tests or groups. All selected tests run by default even
after a failure; --fail-fast stops starting subsequent tests. Results and native
logs are retained under the test work directory, with optional aggregated JUnit.`,
		Example: `  azldev component test curl
  azldev component test curl --execution container
  azldev component test curl --rebuild --base-image vm-base
  azldev component test curl --rpm './out/curl*.rpm' --base-image-path ./base.qcow2
  azldev component test curl --execution ssh --ssh-host test-vm --ssh-user tester`,
		Args: cobra.ExactArgs(1),
		RunE: azldev.RunFuncWithExtraArgs(func(env *azldev.Env, args []string) (interface{}, error) {
			if provision != "" {
				if options.Strategy != "" {
					return nil, errors.New("use only one of '--execution' and deprecated '--provision'")
				}

				switch provision {
				case "virtual":
					options.Strategy = "vm"
				case "local":
					options.Strategy = "host"
				default:
					return nil, fmt.Errorf("unsupported legacy provisioner %#q; use '--execution'", provision)
				}
			}

			return runComponentTests(env, args[0], *options, fromSpec)
		}),
	}
	testexecution.RegisterFlags(cmd, options, true)
	cmd.Flags().BoolVar(&fromSpec, "from-spec", false, "Use the component's rendered spec directory as TMT source")
	_ = cmd.Flags().MarkDeprecated("from-spec", "use --source-dir")
	cmd.Flags().StringVar(&provision, "provision", "", "Legacy alias: virtual=vm, local=host")
	_ = cmd.Flags().MarkDeprecated("provision", "use --execution")

	return cmd
}

func runComponentTests(env *azldev.Env, name string, options testexecution.Options,
	fromSpec bool,
) ([]testframework.RunResult, error) {
	filter := &components.ComponentFilter{ComponentNamePatterns: []string{name}}

	set, err := components.NewResolver(env).FindComponents(filter)
	if err != nil {
		return nil, fmt.Errorf("run component tests:\n%w", err)
	}

	if set.Len() != 1 {
		return nil, fmt.Errorf("expected exactly one component named %#q, found %d", name, set.Len())
	}

	component := set.Components()[0]

	if fromSpec {
		if options.SourceDir != "" {
			return nil, errors.New("'--from-spec' cannot be combined with '--source-dir'")
		}

		options.SourceDir = component.GetConfig().RenderedSpecDir
		if options.SourceDir == "" {
			return nil, errors.New("'--from-spec' requires a rendered spec directory")
		}
	}

	associated, err := env.Config().ResolveComponentTests(component.GetConfig())
	if err != nil {
		return nil, fmt.Errorf("run component tests:\n%w", err)
	}

	if len(associated) == 0 && len(options.Tests) == 0 && options.SourceDir != "" {
		associated = []projectconfig.ResolvedTest{{
			Name: component.GetName(), Definition: projectconfig.TestDefinition{Type: "tmt", Tmt: map[string]any{}},
		}}
	}

	tests, err := testexecution.Select(env.Config(), associated, options.Tests)
	if err != nil {
		return nil, fmt.Errorf("run component tests:\n%w", err)
	}

	_, distro, err := env.Distro()
	if err != nil {
		return nil, fmt.Errorf("run component tests:\n%w", err)
	}

	resolver := &artifacts.Resolver{
		Env: env,
		BuildPackages: func(_ string, arch string) ([]string, error) {
			results, err := BuildComponents(env, set, &ComponentBuildOptions{
				BuildEnvPolicy: BuildEnvPreserveOnFailure, MockConfigOpts: map[string]string{"target_arch": arch},
			})
			if err != nil {
				return nil, fmt.Errorf("run component tests:\n%w", err)
			}

			if len(results) != 1 {
				return nil, errors.New("component builder did not return exactly one build result")
			}

			return results[0].RPMPaths, nil
		},
		BuildImage: imagebuilder.ArtifactBuilder(env),
	}

	results, err := testexecution.Run(env,
		testexecution.Subject{Name: component.GetName(), Kind: "component", Distro: distro},
		tests, options, resolver)
	if err != nil {
		return results, fmt.Errorf("run component tests:\n%w", err)
	}

	return results, nil
}
