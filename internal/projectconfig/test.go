// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package projectconfig

import (
	"errors"
	"fmt"

	"dario.cat/mergo"
)

// TestType indicates the type of test framework used to run a test suite.
type TestType string

const (
	// TestTypePytest uses pytest to run static/offline validation checks.
	TestTypePytest TestType = "pytest"
	// TestTypeLisa uses the LISA framework to run live VM tests.
	TestTypeLisa TestType = "lisa"
)

var (
	// ErrDuplicateTests is returned when duplicate conflicting test definitions are found.
	ErrDuplicateTests = errors.New("duplicate test")
	// ErrUnknownTestType is returned for unrecognized test types.
	ErrUnknownTestType = errors.New("unknown test type")
	// ErrMissingTestField is returned when a required test config field is missing.
	ErrMissingTestField = errors.New("missing required test field")
	// ErrUndefinedTest is returned when an image references a test name that is not defined.
	ErrUndefinedTest = errors.New("undefined test reference")
	// ErrMismatchedTestSubtable is returned when a test config has a subtable that does not
	// match its declared type.
	ErrMismatchedTestSubtable = errors.New("mismatched test subtable")
)

// TestConfig defines a named test suite.
type TestConfig struct {
	// The test's name; not present in serialized TOML files (populated from the map key).
	Name string `toml:"-" json:"name" table:",sortkey"`

	// Description of the test suite.
	Description string `toml:"description,omitempty" json:"description,omitempty" jsonschema:"title=Description,description=Description of this test suite"`

	// Type indicates the test framework to use.
	Type TestType `toml:"type" json:"type" jsonschema:"required,enum=pytest lisa,title=Type,description=Type of test framework (pytest or lisa)"`

	// Pytest holds pytest-specific configuration. Required when Type is "pytest".
	Pytest *PytestConfig `toml:"pytest,omitempty" json:"pytest,omitempty" jsonschema:"title=Pytest config,description=Pytest-specific configuration (required when type is pytest)"`

	// Lisa holds LISA-specific configuration. Required when Type is "lisa".
	Lisa *LisaConfig `toml:"lisa,omitempty" json:"lisa,omitempty" jsonschema:"title=LISA config,description=LISA-specific configuration (required when type is lisa)"`

	// Reference to the source config file that this definition came from; not present
	// in serialized files.
	SourceConfigFile *ConfigFile `toml:"-" json:"-" table:"-"`
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
}

// LisaConfig holds configuration specific to LISA-based test suites.
type LisaConfig struct {
	// RunbookPath is the path to a LISA runbook YAML file.
	RunbookPath string `toml:"runbook" json:"runbook" jsonschema:"required,title=Runbook path,description=Path to the LISA runbook file"`

	// AdminPrivateKeyPath is the path to the admin SSH private key file for LISA.
	AdminPrivateKeyPath string `toml:"admin-private-key-path" json:"adminPrivateKeyPath" jsonschema:"required,title=Admin private key path,description=Path to the admin SSH private key file"`
}

// Validate checks that the test config has valid type-specific required fields and that
// only the matching subtable is present.
func (t *TestConfig) Validate() error {
	switch t.Type {
	case TestTypePytest:
		if t.Pytest == nil {
			return fmt.Errorf("%w: test %#q of type %#q requires a [pytest] subtable",
				ErrMissingTestField, t.Name, t.Type)
		}

		if t.Lisa != nil {
			return fmt.Errorf("%w: test %#q of type %#q must not have a [lisa] subtable",
				ErrMismatchedTestSubtable, t.Name, t.Type)
		}

	case TestTypeLisa:
		if t.Lisa == nil {
			return fmt.Errorf("%w: test %#q of type %#q requires a [lisa] subtable",
				ErrMissingTestField, t.Name, t.Type)
		}

		if t.Lisa.RunbookPath == "" {
			return fmt.Errorf("%w: test %#q of type %#q requires 'runbook'",
				ErrMissingTestField, t.Name, t.Type)
		}

		if t.Lisa.AdminPrivateKeyPath == "" {
			return fmt.Errorf("%w: test %#q of type %#q requires 'admin-private-key-path'",
				ErrMissingTestField, t.Name, t.Type)
		}

		if t.Pytest != nil {
			return fmt.Errorf("%w: test %#q of type %#q must not have a [pytest] subtable",
				ErrMismatchedTestSubtable, t.Name, t.Type)
		}

	default:
		return fmt.Errorf("%w: %#q (test: %#q)", ErrUnknownTestType, t.Type, t.Name)
	}

	return nil
}

// MergeUpdatesFrom updates the test config with overrides present in other.
func (t *TestConfig) MergeUpdatesFrom(other *TestConfig) error {
	err := mergo.Merge(t, other, mergo.WithOverride, mergo.WithAppendSlice)
	if err != nil {
		return fmt.Errorf("failed to merge test config:\n%w", err)
	}

	return nil
}

// WithAbsolutePaths returns a copy of the test config with relative file paths converted
// to absolute paths (relative to referenceDir).
func (t *TestConfig) WithAbsolutePaths(referenceDir string) *TestConfig {
	result := &TestConfig{
		Name:             t.Name,
		Description:      t.Description,
		Type:             t.Type,
		SourceConfigFile: t.SourceConfigFile,
	}

	if t.Pytest != nil {
		result.Pytest = &PytestConfig{
			WorkingDir: makeAbsolute(referenceDir, t.Pytest.WorkingDir),
			TestPaths:  t.Pytest.TestPaths,
			ExtraArgs:  t.Pytest.ExtraArgs,
		}
	}

	if t.Lisa != nil {
		result.Lisa = &LisaConfig{
			RunbookPath:         makeAbsolute(referenceDir, t.Lisa.RunbookPath),
			AdminPrivateKeyPath: makeAbsolute(referenceDir, t.Lisa.AdminPrivateKeyPath),
		}
	}

	return result
}
