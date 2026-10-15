// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package testframework defines the inputs and outcomes shared by external test frameworks.
package testframework

import (
	"errors"
	"fmt"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/artifact"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
)

// Strategy describes the test environment, not the host running the framework.
type Strategy string

const (
	VM        Strategy = "vm" //nolint:varnamelen // VM is the conventional virtual-machine acronym.
	Container Strategy = "container"
	SSH       Strategy = "ssh"
	Host      Strategy = "host"
)

// ExecutionSpec selects an environment and optional developer source overrides.
type ExecutionSpec struct {
	Strategy     Strategy
	SourceDir    string
	FrameworkDir string
	Plan         string
	SSHHost      string
	SSHUser      string
	SSHKey       string
	SSHPort      int
}

// Validate rejects incomplete or contradictory execution requests.
func (s ExecutionSpec) Validate() error {
	switch s.Strategy {
	case VM, Container, Host:
		if s.SSHHost != "" || s.SSHUser != "" || s.SSHKey != "" || s.SSHPort != 0 {
			return errors.New("SSH options require execution strategy 'ssh'")
		}
	case SSH:
		if s.SSHHost == "" || s.SSHUser == "" {
			return errors.New("execution strategy 'ssh' requires a host and user")
		}

		if s.SSHPort < 0 || s.SSHPort > 65535 {
			return errors.New("SSH port must be between 1 and 65535")
		}
	default:
		return fmt.Errorf("unsupported execution strategy %#q", s.Strategy)
	}

	return nil
}

// Requirements are resolved before artifact acquisition or environment setup.
type Requirements struct {
	ImageRequired bool
	ImageFormats  []string
	// LiveImage means the image is booted/run, rather than inspected as a file.
	LiveImage bool
	// PackagesSupported means candidate packages can be supplied to this runner.
	PackagesSupported bool
}

// RunRequest contains materialized inputs; runners never decide whether to build or download them.
type RunRequest struct {
	Execution    ExecutionSpec
	SubjectName  string
	Image        *artifact.Artifact
	Packages     []artifact.Artifact
	Capabilities []string
	Properties   map[string]string
	WorkDir      string
	CacheDir     string
	JUnitPath    string
	InputsPath   string
}

// ImagePath returns the native file or image handle while retaining its metadata in Image.
func (r RunRequest) ImagePath() string {
	if r.Image == nil {
		return ""
	}

	return r.Image.Path
}

// RPMPaths returns the native package arguments without discarding their provenance.
func (r RunRequest) RPMPaths() []string {
	paths := make([]string, 0, len(r.Packages))
	for _, pkg := range r.Packages {
		paths = append(paths, pkg.Path)
	}

	return paths
}

type Status string

const (
	Passed    Status = "passed"
	Failed    Status = "failed"
	Errored   Status = "error"
	Skipped   Status = "skipped"
	Cancelled Status = "cancelled"
	Planned   Status = "planned"
)

// RunResult preserves the native report location as well as the normalized outcome.
type RunResult struct {
	Name      string        `json:"name"                table:",sortkey"`
	Framework string        `json:"framework"`
	Strategy  Strategy      `json:"strategy"`
	Status    Status        `json:"status"`
	Duration  time.Duration `json:"duration"`
	OutputDir string        `json:"outputDir"`
	JUnitPath string        `json:"junitPath,omitempty"`
	Error     string        `json:"error,omitempty"     table:"-"`
}

// ErrTestFailed distinguishes failing assertions from setup and execution errors.
var ErrTestFailed = errors.New("test assertions failed")

// Runner is implemented by a configured framework adapter.
type Runner interface {
	// Requirements validates strategy support and describes required artifacts.
	Requirements(execution ExecutionSpec) (Requirements, error)
	// Run executes against materialized inputs and retains native reports.
	Run(ctx opctx.Ctx, request RunRequest) (RunResult, error)
}

// ValidateName keeps managed test directories below their run directory.
func ValidateName(name string) error {
	if err := artifact.ValidateName(name); err != nil {
		return fmt.Errorf("invalid test name:\n%w", err)
	}

	return nil
}
