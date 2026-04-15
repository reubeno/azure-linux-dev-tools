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
)

// TestConfig defines a named test suite.
type TestConfig struct {
	// The test's name; not present in serialized TOML files (populated from the map key).
	Name string `toml:"-" json:"name" table:",sortkey"`

	// Description of the test suite.
	Description string `toml:"description,omitempty" json:"description,omitempty" jsonschema:"title=Description,description=Description of this test suite"`

	// Type indicates the test framework to use.
	Type TestType `toml:"type" json:"type" jsonschema:"required,enum=pytest lisa,title=Type,description=Type of test framework (pytest or lisa)"`

	// TestDir is the path to the directory containing pytest test files.
	// Required when Type is "pytest".
	TestDir string `toml:"test-dir,omitempty" json:"testDir,omitempty" jsonschema:"title=Test directory,description=Path to the directory containing pytest test files (required for pytest type)"`

	// RunbookPath is the path to a LISA runbook YAML file.
	// Required when Type is "lisa".
	RunbookPath string `toml:"runbook,omitempty" json:"runbook,omitempty" jsonschema:"title=Runbook path,description=Path to the LISA runbook file (required for lisa type)"`

	// AdminPrivateKeyPath is the path to the admin SSH private key file for LISA.
	// Required when Type is "lisa".
	AdminPrivateKeyPath string `toml:"admin-private-key-path,omitempty" json:"adminPrivateKeyPath,omitempty" jsonschema:"title=Admin private key path,description=Path to the admin SSH private key file (required for lisa type)"`

	// MockPackages lists additional RPM packages to install in the mock chroot
	// before running pytest tests.
	MockPackages []string `toml:"mock-packages,omitempty" json:"mockPackages,omitempty" jsonschema:"title=Mock packages,description=Additional RPM packages to install in the mock chroot for pytest tests"`

	// Reference to the source config file that this definition came from; not present
	// in serialized files.
	SourceConfigFile *ConfigFile `toml:"-" json:"-" table:"-"`
}

// Validate checks that the test config has valid type-specific required fields.
func (t *TestConfig) Validate() error {
	switch t.Type {
	case TestTypePytest:
		if t.TestDir == "" {
			return fmt.Errorf("%w: test %#q of type %#q requires 'test-dir'",
				ErrMissingTestField, t.Name, t.Type)
		}

	case TestTypeLisa:
		if t.RunbookPath == "" {
			return fmt.Errorf("%w: test %#q of type %#q requires 'runbook'",
				ErrMissingTestField, t.Name, t.Type)
		}

		if t.AdminPrivateKeyPath == "" {
			return fmt.Errorf("%w: test %#q of type %#q requires 'admin-private-key-path'",
				ErrMissingTestField, t.Name, t.Type)
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
		Name:                t.Name,
		Description:         t.Description,
		Type:                t.Type,
		TestDir:             makeAbsolute(referenceDir, t.TestDir),
		RunbookPath:         makeAbsolute(referenceDir, t.RunbookPath),
		AdminPrivateKeyPath: makeAbsolute(referenceDir, t.AdminPrivateKeyPath),
		MockPackages:        t.MockPackages,
		SourceConfigFile:    t.SourceConfigFile,
	}

	return result
}

// ImageTestsConfig holds the test references for an image.
type ImageTestsConfig struct {
	// Tests is the list of test names (referencing top-level [tests.*] entries) that
	// apply to this image.
	Tests []string `toml:"tests,omitempty" json:"tests,omitempty" jsonschema:"title=Tests,description=List of test suite names that apply to this image"`
}
