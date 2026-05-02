// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package projectconfig

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"dario.cat/mergo"
)

// TestType indicates the type of test framework used to run a test suite.
type TestType string

const (
	// TestTypePytest uses pytest to run static/offline validation checks.
	TestTypePytest TestType = "pytest"
	// TestTypeTmt uses tmt (Fedora Test Management Tool) to run plans defined in
	// dist-git or any FMF-shaped repo.
	TestTypeTmt TestType = "tmt"
)

// commitSHALength is the canonical length of a git SHA-1 hex commit hash.
const commitSHALength = 40

var (
	// ErrDuplicateTestSuites is returned when duplicate conflicting test suite definitions are found.
	ErrDuplicateTestSuites = errors.New("duplicate test suite")
	// ErrUnknownTestType is returned for unrecognized test types.
	ErrUnknownTestType = errors.New("unknown test type")
	// ErrMissingTestField is returned when a required test config field is missing.
	ErrMissingTestField = errors.New("missing required test field")
	// ErrUndefinedTestSuite is returned when an image references a test suite name that is not defined.
	ErrUndefinedTestSuite = errors.New("undefined test suite reference")
	// ErrMismatchedTestSubtable is returned when a test config has a subtable that does not
	// match its declared type. Currently only one test type (pytest) exists, so this cannot
	// trigger yet. When adding a new test type with its own subtable field, add cross-checks
	// in [TestSuiteConfig.Validate] to ensure only the matching subtable is populated.
	ErrMismatchedTestSubtable = errors.New("mismatched test subtable")
	// ErrInvalidInstallMode is returned when a [PytestConfig.Install] value is not recognized.
	ErrInvalidInstallMode = errors.New("invalid install mode")
	// ErrInvalidGitRef is returned when a git ref is not a valid full-length hex commit SHA.
	ErrInvalidGitRef = errors.New("invalid git ref")
	// ErrUnsupportedTmtProvision is returned when a [TmtProvisionConfig.How] value is not
	// supported in this azldev version.
	ErrUnsupportedTmtProvision = errors.New("unsupported tmt provision how")
	// ErrForbiddenExtraArg is returned when a forbidden flag (managed by azldev) appears in a *-extra-args list.
	ErrForbiddenExtraArg = errors.New("forbidden extra arg")
)

// TestSuiteConfig defines a named test suite.
type TestSuiteConfig struct {
	// The test suite's name; not present in serialized TOML files (populated from the map key).
	Name string `toml:"-" json:"name" table:",sortkey"`

	// Description of the test suite.
	Description string `toml:"description,omitempty" json:"description,omitempty" jsonschema:"title=Description,description=Description of this test suite"`

	// Type indicates the test framework to use.
	Type TestType `toml:"type" json:"type" jsonschema:"required,enum=pytest,enum=tmt,title=Type,description=Type of test framework (pytest or tmt)"`

	// Pytest holds pytest-specific configuration. Required when Type is "pytest".
	Pytest *PytestConfig `toml:"pytest,omitempty" json:"pytest,omitempty" jsonschema:"title=Pytest config,description=Pytest-specific configuration (required when type is pytest)"`

	// Tmt holds tmt-specific configuration. Required when Type is "tmt".
	Tmt *TmtConfig `toml:"tmt,omitempty" json:"tmt,omitempty" jsonschema:"title=Tmt config,description=tmt-specific configuration (required when type is tmt)"`

	// Reference to the source config file that this definition came from; not present
	// in serialized files.
	SourceConfigFile *ConfigFile `toml:"-" json:"-" table:"-"`
}

// PytestInstallMode specifies how Python dependencies are installed for a pytest suite.
type PytestInstallMode string

const (
	// PytestInstallPyproject installs dependencies from pyproject.toml using editable mode.
	// Returns an error if pyproject.toml is not found in the working directory.
	PytestInstallPyproject PytestInstallMode = "pyproject"
	// PytestInstallRequirements installs dependencies from requirements.txt.
	// Returns an error if requirements.txt is not found.
	PytestInstallRequirements PytestInstallMode = "requirements"
	// PytestInstallNone skips dependency installation entirely. This is the default
	// when [PytestConfig.Install] is not specified — pytest must already be available
	// in the venv (e.g., pre-installed, or installed by the test author out-of-band).
	PytestInstallNone PytestInstallMode = "none"
)

// TmtProvisionHow identifies the tmt provision plugin to use ([tmt provision -h <how>]).
type TmtProvisionHow string

const (
	// TmtProvisionHowVirtual launches a libvirt/qemu VM via testcloud.
	// Requires the cloud-init-friendly image and the `provision-virtual` pip extra.
	TmtProvisionHowVirtual TmtProvisionHow = "virtual"
)

// TmtGitSource identifies a git repository at a specific commit SHA.
type TmtGitSource struct {
	// GitURL is the URL of the git repository.
	GitURL string `toml:"git-url" json:"gitUrl" jsonschema:"required,title=Git URL,description=URL of the git repository"`

	// Ref is the commit SHA to check out. Must be a 40-character hex commit hash.
	Ref string `toml:"ref" json:"ref" jsonschema:"required,title=Ref,description=Commit SHA to check out (40-char hex hash)"`
}

// Validate checks that [TmtGitSource] has required fields and a valid 40-char hex SHA.
func (g *TmtGitSource) Validate(context string) error {
	if g.GitURL == "" {
		return fmt.Errorf("%w: %s requires 'git-url'", ErrMissingTestField, context)
	}

	if g.Ref == "" {
		return fmt.Errorf("%w: %s requires 'ref'", ErrMissingTestField, context)
	}

	return validateCommitSHA(context, g.Ref)
}

// validateCommitSHA returns an error unless ref is a 40-char hex string.
func validateCommitSHA(context string, ref string) error {
	if len(ref) != commitSHALength {
		return fmt.Errorf("%w: %s expected %d hex characters, got %d: %#q",
			ErrInvalidGitRef, context, commitSHALength, len(ref), ref)
	}

	if _, err := hex.DecodeString(ref); err != nil {
		return fmt.Errorf("%w: %s not a valid hex string: %#q",
			ErrInvalidGitRef, context, ref)
	}

	return nil
}

// TmtProvisionConfig holds the projection of tmt's `provision` step. MVP only projects
// `how`; additional knobs (memory, disk, etc.) are handled via [TmtConfig.ProvisionExtraArgs].
type TmtProvisionConfig struct {
	// How selects the tmt provision plugin. Optional; if empty, tmt's default applies.
	// MVP supports "virtual" only.
	How TmtProvisionHow `toml:"how,omitempty" json:"how,omitempty" jsonschema:"enum=virtual,title=How,description=tmt provision plugin (MVP supports only 'virtual')"`
}

// TmtConfig holds configuration specific to tmt-based test suites.
type TmtConfig struct {
	// Source identifies the git repository (with FMF metadata) that contains the plan(s).
	Source TmtGitSource `toml:"source" json:"source" jsonschema:"required,title=Source,description=Git source for the tmt content (e.g., a dist-git repo)"`

	// Plan is the tmt plan name to run. Required: MVP must select exactly one plan.
	// Passed to tmt as: plan -n <plan>.
	Plan string `toml:"plan" json:"plan" jsonschema:"required,title=Plan,description=tmt plan name (e.g., '/plans/shell'). MVP requires exactly one plan."`

	// PipExtras lists additional pip extras to install alongside tmt. The runner
	// auto-derives the extra required by [TmtProvisionConfig.How] (e.g., "provision-virtual"
	// for how="virtual") and unions it with this list.
	PipExtras []string `toml:"pip-extras,omitempty" json:"pipExtras,omitempty" jsonschema:"title=Pip extras,description=Additional pip extras to install with tmt (provision-virtual is auto-added when how=virtual)"`

	// Context is a multi-valued map of tmt context dimensions (passed via tmt -c k=v).
	// tmt context dimensions can be multi-valued (e.g., distro=[fedora-39, fedora-40]),
	// so each map value is a slice. The runner emits one -c k=v per (key, value) pair.
	Context map[string][]string `toml:"context,omitempty" json:"context,omitempty" jsonschema:"title=Context,description=tmt context dimensions (e.g., distro arch component); multi-valued"`

	// RunExtraArgs are passed verbatim before the tmt 'run' subcommand.
	// Forbidden flags: --id, -c, --context (managed by azldev).
	RunExtraArgs []string `toml:"run-extra-args,omitempty" json:"runExtraArgs,omitempty" jsonschema:"title=Run extra args,description=Verbatim args inserted before 'tmt run' (cannot include --id\\, -c\\, --context)"`

	// PlanExtraArgs are passed verbatim after `plan -n <plan>`.
	PlanExtraArgs []string `toml:"plan-extra-args,omitempty" json:"planExtraArgs,omitempty" jsonschema:"title=Plan extra args,description=Verbatim args inserted after 'plan -n <plan>'"`

	// ProvisionExtraArgs are passed verbatim after `provision -h <how>`.
	// Forbidden flags: --image (managed by azldev to inject the image-under-test).
	ProvisionExtraArgs []string `toml:"provision-extra-args,omitempty" json:"provisionExtraArgs,omitempty" jsonschema:"title=Provision extra args,description=Verbatim args inserted after 'provision -h <how>' (cannot include --image)"`

	// CloudInitRuncmds is a list of shell commands to run during cloud-init's runcmd
	// phase on the provisioned guest. Each entry is one runcmd line; the runner injects
	// them via tmt's TMT_PLUGINS extension point (a small Python file that appends to
	// tmt.steps.provision.testcloud.TESTCLOUD_WORKAROUNDS, which tmt then funnels into
	// cloud-init's runcmd before testcloud's boot-complete probe runs). This is the
	// place to put first-boot fixups that must complete BEFORE tmt considers the guest
	// "ready" — e.g., opening a firewall port that testcloud's HTTP readiness probe
	// needs. For host-side fixups or anything that should happen AFTER provision, use
	// tmt's `prepare` step instead.
	CloudInitRuncmds []string `toml:"cloud-init-runcmds,omitempty" json:"cloudInitRuncmds,omitempty" jsonschema:"title=Cloud-init runcmds,description=Shell commands run during cloud-init runcmd on the guest (before tmt's boot-complete probe). Use for first-boot fixups (e.g., open a firewall port)."`

	// Provision configures the tmt provision step. Only 'how' is projected for MVP.
	Provision TmtProvisionConfig `toml:"provision,omitempty" json:"provision,omitempty" jsonschema:"title=Provision,description=tmt provision step configuration"`
}

// Forbidden flags inside *-extra-args slices, by step. Names are normalized to the leading
// flag-token (the part before any '=').
//
//nolint:gochecknoglobals // Effectively constants; Go has no const slices.
var (
	forbiddenRunExtraArgs       = []string{"--id", "-i", "-c", "--context"}
	forbiddenProvisionExtraArgs = []string{"--image"}
	// PlanExtraArgs has no forbidden flags today (azldev sets `-n <plan>` itself, but if
	// a user passes another `-n` here tmt itself will reject the duplication).
)

// Validate checks that [TmtConfig] has required fields and that all *-extra-args lists
// avoid azldev-managed flags.
func (t *TmtConfig) Validate(suiteName string) error {
	if err := t.Source.Validate(fmt.Sprintf("test suite %#q tmt.source", suiteName)); err != nil {
		return err
	}

	if t.Plan == "" {
		return fmt.Errorf("%w: test suite %#q requires 'tmt.plan' (MVP must select exactly one plan)",
			ErrMissingTestField, suiteName)
	}

	if t.Provision.How != "" && !t.Provision.How.isSupported() {
		return fmt.Errorf("%w: test suite %#q tmt.provision.how=%#q "+
			"(MVP supports only %#q)",
			ErrUnsupportedTmtProvision, suiteName, t.Provision.How, TmtProvisionHowVirtual)
	}

	if err := validateExtraArgs(suiteName, "tmt.run-extra-args", t.RunExtraArgs, forbiddenRunExtraArgs); err != nil {
		return err
	}

	if err := validateExtraArgs(suiteName, "tmt.provision-extra-args",
		t.ProvisionExtraArgs, forbiddenProvisionExtraArgs); err != nil {
		return err
	}

	return nil
}

// isSupported returns whether the [TmtProvisionHow] value is implemented in this azldev version.
func (h TmtProvisionHow) isSupported() bool {
	switch h {
	case TmtProvisionHowVirtual:
		return true
	default:
		return false
	}
}

// validateExtraArgs rejects entries in args that match (after splitting on '=') any of
// forbidden. Comparisons are case-sensitive (Linux flag conventions).
func validateExtraArgs(suiteName string, fieldName string, args []string, forbidden []string) error {
	for _, arg := range args {
		token, _, _ := strings.Cut(arg, "=")
		for _, bad := range forbidden {
			if token == bad {
				return fmt.Errorf("%w: test suite %#q %s contains %#q "+
					"(this flag is managed by azldev and cannot be overridden)",
					ErrForbiddenExtraArg, suiteName, fieldName, arg)
			}
		}
	}

	return nil
}

// PytestConfig holds configuration specific to pytest-based test suites.
type PytestConfig struct {
	// WorkingDir is the directory to use as the current working directory when running pytest.
	// Relative paths are resolved against the config file's directory.
	WorkingDir string `toml:"working-dir,omitempty" json:"workingDir,omitempty" jsonschema:"title=Working directory,description=Directory to use as CWD when running pytest"`

	// TestPaths is the list of test file paths or directories to pass to pytest as positional
	// arguments. Glob patterns (e.g., cases/test_*.py) are expanded relative to WorkingDir.
	TestPaths []string `toml:"test-paths,omitempty" json:"testPaths,omitempty" jsonschema:"title=Test paths,description=Test file paths or directories passed to pytest. Glob patterns are expanded."`

	// ExtraArgs is the list of additional arguments to pass to pytest. These are passed
	// verbatim after placeholder substitution. Use {image-path} as a placeholder for the
	// image path, which will be substituted at runtime.
	ExtraArgs []string `toml:"extra-args,omitempty" json:"extraArgs,omitempty" jsonschema:"title=Extra arguments,description=Additional arguments passed to pytest. Use {image-path} as a placeholder for the image path."`

	// Install specifies how Python dependencies are installed into the venv before running
	// pytest. Defaults to "none" (no install) when not specified.
	Install PytestInstallMode `toml:"install,omitempty" json:"install,omitempty" jsonschema:"enum=pyproject,enum=requirements,enum=none,title=Install mode,description=How to install Python dependencies: pyproject\\, requirements\\, or none (default)"`
}

// Validate checks that the test suite config has valid type-specific required fields and that
// only the matching subtable is present.
func (t *TestSuiteConfig) Validate() error {
	if t.Type == "" {
		return fmt.Errorf("%w: test suite %#q is missing required field 'type'",
			ErrMissingTestField, t.Name)
	}

	switch t.Type {
	case TestTypePytest:
		if t.Pytest == nil {
			return fmt.Errorf("%w: test suite %#q of type %#q requires a [pytest] subtable",
				ErrMissingTestField, t.Name, t.Type)
		}

		if t.Tmt != nil {
			return fmt.Errorf("%w: test suite %#q of type %#q must not have a [tmt] subtable",
				ErrMismatchedTestSubtable, t.Name, t.Type)
		}

		if err := t.Pytest.Validate(); err != nil {
			return fmt.Errorf("test suite %#q: %w", t.Name, err)
		}

	case TestTypeTmt:
		if t.Tmt == nil {
			return fmt.Errorf("%w: test suite %#q of type %#q requires a [tmt] subtable",
				ErrMissingTestField, t.Name, t.Type)
		}

		if t.Pytest != nil {
			return fmt.Errorf("%w: test suite %#q of type %#q must not have a [pytest] subtable",
				ErrMismatchedTestSubtable, t.Name, t.Type)
		}

		if err := t.Tmt.Validate(t.Name); err != nil {
			return err
		}

	default:
		return fmt.Errorf("%w: %#q (test suite: %#q)", ErrUnknownTestType, t.Type, t.Name)
	}

	return nil
}

// Validate checks that the [PytestConfig] fields are valid.
func (p *PytestConfig) Validate() error {
	if p.Install != "" && !p.Install.isValid() {
		return fmt.Errorf(
			"%w: %#q; allowed values: %#q, %#q, %#q (or omit for default %#q)",
			ErrInvalidInstallMode, p.Install,
			PytestInstallPyproject, PytestInstallRequirements, PytestInstallNone,
			PytestInstallNone,
		)
	}

	// When the effective install mode requires a working directory, 'working-dir'
	// must be specified. The default mode is 'none' (no install) and so requires
	// nothing; only an explicitly-set install mode that performs work needs the dir.
	if p.EffectiveInstallMode() != PytestInstallNone && p.WorkingDir == "" {
		return fmt.Errorf(
			"%w: 'working-dir' is required when install mode is %#q",
			ErrMissingTestField, p.EffectiveInstallMode(),
		)
	}

	return nil
}

// EffectiveInstallMode returns the install mode, defaulting to [PytestInstallNone] when
// the field is not set.
func (p *PytestConfig) EffectiveInstallMode() PytestInstallMode {
	if p.Install == "" {
		return PytestInstallNone
	}

	return p.Install
}

// isValid returns whether the mode is a recognized [PytestInstallMode] value.
func (m PytestInstallMode) isValid() bool {
	switch m {
	case PytestInstallPyproject, PytestInstallRequirements, PytestInstallNone:
		return true
	default:
		return false
	}
}

// MergeUpdatesFrom updates the test suite config with overrides present in other.
func (t *TestSuiteConfig) MergeUpdatesFrom(other *TestSuiteConfig) error {
	err := mergo.Merge(t, other, mergo.WithOverride, mergo.WithAppendSlice)
	if err != nil {
		return fmt.Errorf("failed to merge test suite config:\n%w", err)
	}

	return nil
}

// WithAbsolutePaths returns a copy of the test suite config with relative file paths converted
// to absolute paths (relative to referenceDir).
func (t *TestSuiteConfig) WithAbsolutePaths(referenceDir string) *TestSuiteConfig {
	result := &TestSuiteConfig{
		Name:             t.Name,
		Description:      t.Description,
		Type:             t.Type,
		SourceConfigFile: t.SourceConfigFile,
	}

	if t.Pytest != nil {
		result.Pytest = &PytestConfig{
			WorkingDir: makeAbsolute(referenceDir, t.Pytest.WorkingDir),
			TestPaths:  append([]string(nil), t.Pytest.TestPaths...),
			ExtraArgs:  append([]string(nil), t.Pytest.ExtraArgs...),
			Install:    t.Pytest.Install,
		}
	}

	if t.Tmt != nil {
		// Deep copy. No path fields today (Source.GitURL is a URL, not a path);
		// this method exists for symmetry and to safely deep-copy slices/maps.
		ctxCopy := make(map[string][]string, len(t.Tmt.Context))
		for k, v := range t.Tmt.Context {
			ctxCopy[k] = append([]string(nil), v...)
		}

		result.Tmt = &TmtConfig{
			Source:             t.Tmt.Source,
			Plan:               t.Tmt.Plan,
			PipExtras:          append([]string(nil), t.Tmt.PipExtras...),
			Context:            ctxCopy,
			RunExtraArgs:       append([]string(nil), t.Tmt.RunExtraArgs...),
			PlanExtraArgs:      append([]string(nil), t.Tmt.PlanExtraArgs...),
			ProvisionExtraArgs: append([]string(nil), t.Tmt.ProvisionExtraArgs...),
			CloudInitRuncmds:   append([]string(nil), t.Tmt.CloudInitRuncmds...),
			Provision:          t.Tmt.Provision,
		}
	}

	return result
}
