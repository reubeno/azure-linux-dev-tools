// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package component

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/components"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/sources"
	"github.com/microsoft/azure-linux-dev-tools/internal/providers/sourceproviders"
	"github.com/spf13/cobra"
)

// GenerateSpecOptions holds options for the "component generate-spec" command.
type GenerateSpecOptions struct {
	ComponentFilter components.ComponentFilter
}

func generateSpecOnAppInit(_ *azldev.App, sourceCmd *cobra.Command) {
	sourceCmd.AddCommand(NewGenerateSpecCmd())
}

// NewGenerateSpecCmd constructs a [cobra.Command] for "component generate-spec" CLI subcommand.
func NewGenerateSpecCmd() *cobra.Command {
	var options GenerateSpecOptions

	cmd := &cobra.Command{
		Use:   "generate-spec",
		Short: "Generate overlay-applied spec files for components",
		Long: `Generate spec files for the selected components by fetching the upstream spec
and sidecar files, then applying any configured overlays. Source tarballs from
lookaside caches are not downloaded.

Each component's output is written to a subdirectory of the project's
generated-specs-dir setting. The output directory for each component is
deleted and recreated on every run.

One or more components may be selected at a time.`,
		Example: `  # Generate specs for all components
  azldev component generate-spec -a

  # Generate spec for a single component
  azldev component generate-spec -p curl

  # Generate specs matching a pattern
  azldev component generate-spec -p "azure*"`,
		RunE: azldev.RunFuncWithExtraArgs(func(env *azldev.Env, args []string) (interface{}, error) {
			options.ComponentFilter.ComponentNamePatterns = append(args, options.ComponentFilter.ComponentNamePatterns...)

			return nil, GenerateSpecs(env, &options)
		}),
		ValidArgsFunction: components.GenerateComponentNameCompletions,
		Annotations: map[string]string{
			azldev.CommandAnnotationRootOK: "true",
		},
	}

	components.AddComponentFilterOptionsToCommand(cmd, &options.ComponentFilter)

	return cmd
}

// GenerateSpecs generates overlay-applied spec files for the selected components. It uses force
// semantics, deleting and recreating each component's output directory before generating.
func GenerateSpecs(env *azldev.Env, options *GenerateSpecOptions) error {
	generatedSpecsDir := env.Config().Project.GeneratedSpecsDir
	if generatedSpecsDir == "" {
		return errors.New("generated-specs-dir is not configured in the project settings;\n" +
			"set [project] generated-specs-dir in your project configuration to use this command")
	}

	resolver := components.NewResolver(env)

	comps, err := resolver.FindComponents(&options.ComponentFilter)
	if err != nil {
		return fmt.Errorf("failed to resolve components:\n%w", err)
	}

	if comps.Len() == 0 {
		return errors.New("no components were selected; " +
			"please use command-line options to indicate which components to generate specs for")
	}

	for _, component := range comps.Components() {
		if err := generateSpecForComponent(env, component, generatedSpecsDir); err != nil {
			return fmt.Errorf("failed to generate spec for component %q:\n%w", component.GetName(), err)
		}
	}

	return nil
}

func generateSpecForComponent(
	env *azldev.Env, component components.Component, generatedSpecsDir string,
) error {
	outputDir := filepath.Join(generatedSpecsDir, component.GetName())

	event := env.StartEvent("Generating spec", "component", component.GetName(), "outputDir", outputDir)
	defer event.End()

	// Always force: delete and recreate the output directory.
	if err := env.FS().RemoveAll(outputDir); err != nil {
		return fmt.Errorf("failed to clean output directory %#q:\n%w", outputDir, err)
	}

	distro, err := sourceproviders.ResolveDistro(env, component)
	if err != nil {
		return fmt.Errorf("failed to resolve distro for component %#q:\n%w", component.GetName(), err)
	}

	sourceManager, err := sourceproviders.NewSourceManager(env, distro, sourceproviders.WithSkipLookaside())
	if err != nil {
		return fmt.Errorf("failed to create source manager:\n%w", err)
	}

	preparer, err := sources.NewPreparer(sourceManager, env.FS(), env, env)
	if err != nil {
		return fmt.Errorf("failed to create source preparer:\n%w", err)
	}

	err = preparer.PrepareSources(env, component, outputDir, true)
	if err != nil {
		return fmt.Errorf("failed to prepare sources:\n%w", err)
	}

	return nil
}
