// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package testframework defines the inputs and outcomes shared by external test frameworks.
package testframework

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
)

// Strategy describes the test environment, not the host running the framework.
type Strategy string

const (
	VM        Strategy = "vm"
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
	ImagePath    string
	RPMPaths     []string
	Capabilities []string
	Properties   map[string]string
	WorkDir      string
	CacheDir     string
	JUnitPath    string
	InputsPath   string
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
	Name       string        `json:"name" table:",sortkey"`
	Framework  string        `json:"framework"`
	Strategy   Strategy      `json:"strategy"`
	Status     Status        `json:"status"`
	Duration   time.Duration `json:"duration"`
	OutputDir  string        `json:"outputDir"`
	JUnitPath  string        `json:"junitPath,omitempty"`
	Error      string        `json:"error,omitempty"`
}

// ErrTestFailed distinguishes failing assertions from setup and execution errors.
var ErrTestFailed = errors.New("test assertions failed")

// Runner is implemented by a configured framework adapter.
type Runner interface {
	Requirements(ExecutionSpec) (Requirements, error)
	Run(opctx.Ctx, RunRequest) (RunResult, error)
}

// ValidateName keeps managed test directories below their run directory.
func ValidateName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.IsAbs(name) ||
		filepath.Clean(name) != name || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid test name %#q: expected a simple name without path separators", name)
	}

	return nil
}
