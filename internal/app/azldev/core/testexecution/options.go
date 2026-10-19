// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testexecution

import (
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/artifacts"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/spf13/cobra"
)

// Options is the shared CLI policy; adapters only receive resolved execution inputs.
type Options struct {
	Tests            []string
	Strategy         string
	SourceDir        string
	LisaDir          string
	Plan             string
	SSHHost          string
	SSHUser          string
	SSHKey           string
	SSHPort          int
	ArtifactSource   string
	Rebuild          bool
	ImagePath        string
	RPMPaths         []string
	BaseImage        string
	BaseImageSource  string
	BaseImagePath    string
	RebuildBaseImage bool
	Arch             string
	WorkDir          string
	JUnitXMLPath     string
	FailFast         bool
}

func (o Options) hasSSHOptions() bool {
	return o.SSHHost != "" || o.SSHUser != "" || o.SSHKey != "" || o.SSHPort != 0
}

func (o Options) hasBaseOptions() bool {
	return o.BaseImage != "" || o.BaseImagePath != "" || o.RebuildBaseImage || o.BaseImageSource != ""
}

func (o Options) subjectSelection(kind string) artifacts.Selection {
	result := artifacts.Selection{Source: o.ArtifactSource, Rebuild: o.Rebuild}
	if kind == "component" {
		result.Paths = o.RPMPaths
	} else if o.ImagePath != "" {
		result.Paths = []string{o.ImagePath}
	}

	return result
}

func (o Options) baseSelection() artifacts.Selection {
	result := artifacts.Selection{Source: o.BaseImageSource, Rebuild: o.RebuildBaseImage}
	if o.BaseImagePath != "" {
		result.Paths = []string{o.BaseImagePath}
		// The default LKG policy must not override an explicit path.
		if result.Source == "" {
			result.Source = artifacts.SourcePath
		}
	} else if result.Source == "" {
		if result.Rebuild {
			result.Source = "local"
		} else {
			result.Source = "lkg"
		}
	}

	return result
}

func (o Options) execution(strategy testframework.Strategy) testframework.ExecutionSpec {
	return testframework.ExecutionSpec{
		Strategy: strategy, SourceDir: o.SourceDir, FrameworkDir: o.LisaDir, Plan: o.Plan,
		SSH: testframework.SSHConnection{Host: o.SSHHost, User: o.SSHUser, KeyPath: o.SSHKey, Port: o.SSHPort},
	}
}

// RegisterFlags keeps policy and spelling consistent across component and image commands.
func RegisterFlags(cmd *cobra.Command, options *Options, component bool) {
	flags := cmd.Flags()
	flags.StringSliceVarP(&options.Tests, "test", "t", nil,
		"Test or test-group name (repeatable; defaults to associated tests)")
	flags.StringSliceVar(&options.Tests, "test-suite", nil, "Alias for --test")
	_ = flags.MarkDeprecated("test-suite", "use --test")
	flags.StringVar(&options.Strategy, "execution", "",
		"Execution strategy: vm, container, ssh, or host (framework default when omitted)")
	flags.StringVar(&options.ArtifactSource, "artifact-source", "",
		"Subject artifact source: local (default; reuse or build), lkg, or path")
	flags.BoolVar(&options.Rebuild, "rebuild", false, "Rebuild the subject locally instead of reusing existing output")
	flags.StringVar(&options.Arch, "arch", "", "Target architecture: x86_64 or aarch64 (defaults to host)")
	flags.StringVar(&options.WorkDir, "work-dir", "",
		"Root for isolated test runs (defaults to the project work directory)")
	flags.StringVar(&options.JUnitXMLPath, "junit-xml", "", "Write an aggregated JUnit XML report for all selected tests")
	flags.BoolVar(&options.FailFast, "fail-fast", false, "Stop starting tests after the first failure")
	flags.StringVar(&options.SourceDir, "source-dir", "", "Local test source override instead of cloning (TMT/pytest)")
	flags.StringVar(&options.LisaDir, "lisa-dir", "", "Local LISA framework checkout instead of cloning")
	flags.StringVar(&options.Plan, "plan", "", "TMT plan override")
	flags.StringVar(&options.SSHHost, "ssh-host", "", "Existing SSH target hostname (--execution ssh)")
	flags.StringVar(&options.SSHUser, "ssh-user", "", "Existing SSH target username")
	flags.StringVar(&options.SSHKey, "ssh-key", "", "Private key for the existing SSH target")
	flags.IntVar(&options.SSHPort, "ssh-port", 0, "Existing SSH target port (defaults to 22)")

	if component {
		registerComponentFlags(cmd, options)
		flags.StringVarP(&options.BaseImagePath, "image-path", "i", "", "Alias for --base-image-path")
		_ = flags.MarkDeprecated("image-path", "use --base-image-path")
	} else {
		flags.StringVarP(&options.ImagePath, "image-path", "i", "",
			"Explicit image under test; overrides local build selection")
	}

	for _, name := range []string{"image-path", "base-image-path", "rpm", "ssh-key", "junit-xml"} {
		if flags.Lookup(name) != nil {
			_ = cmd.MarkFlagFilename(name)
		}
	}

	for _, name := range []string{"work-dir", "source-dir", "lisa-dir"} {
		_ = cmd.MarkFlagDirname(name)
	}
}

func registerComponentFlags(cmd *cobra.Command, options *Options) {
	flags := cmd.Flags()
	flags.StringSliceVarP(&options.RPMPaths, "rpm", "r", nil,
		"Explicit binary RPM paths/globs for a component; overrides the complete component build")
	flags.StringVar(&options.BaseImage, "base-image", "",
		"Named supporting image for component tests (defaults from test/distro configuration)")
	flags.StringVar(&options.BaseImageSource, "base-image-source", "",
		"Supporting image source: lkg (default), local, or path")
	flags.StringVar(&options.BaseImagePath, "base-image-path", "", "Explicit supporting VM image or OCI archive path")
	flags.BoolVar(&options.RebuildBaseImage, "rebuild-base-image", false, "Build the named supporting image locally")

	_ = cmd.MarkFlagFilename("rpm")
	_ = cmd.MarkFlagFilename("base-image-path")
}
