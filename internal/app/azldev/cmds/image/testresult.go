// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package image

// ImageTestResult is a single row in the structured output of `azldev image test`.
//
// Each row describes either:
//   - one test executed by a runner (Test is set), or
//   - a suite-level rollup when no per-test results are available, e.g., a tmt provision
//     step failed before any test ran (Test is empty; Status carries the rollup verdict).
//
// The struct is shared across runners (pytest, tmt, and any future framework) so that
// `azldev image test --output table|json` produces a uniform result table. Runners that
// don't yet emit per-test detail (today: pytest) return one rollup row per suite.
type ImageTestResult struct {
	// Suite is the test-suite name (the key into [test-suites] in project config).
	Suite string `json:"suite" table:",sortkey"`
	// Type is the test framework that produced this row (e.g., "tmt", "pytest").
	Type string `json:"type"`
	// Test is the framework-native test identifier (e.g., a tmt path like "/tests/foo",
	// or a pytest nodeid). Empty for suite-level rollup rows.
	Test string `json:"test,omitempty"`
	// Status is the outcome — values follow tmt's vocabulary for cross-runner consistency:
	//   pass, fail, error, info, warn, skip
	// (where "error" is reserved for infrastructure-level failures that prevented the test
	// from running, and "fail" is a legitimate test failure verdict).
	Status string `json:"status"`
	// DurationSeconds is the wall-clock duration of the test, when known. For suite-level
	// rollup rows this is the total runner wall time when available, or 0 otherwise.
	DurationSeconds float64 `json:"durationSeconds,omitempty"`
	// OutputPath is a filesystem path to per-test output (typically tmt's
	// execute/data/<test>/output.txt). Hidden from table output to keep rows narrow;
	// always present in JSON output.
	OutputPath string `json:"outputPath,omitempty" table:"-"`
	// RunDir is the runner-specific directory that holds detailed artifacts for this row
	// (e.g., the tmt run dir). Hidden from table output for the same reason.
	RunDir string `json:"runDir,omitempty" table:"-"`
	// ErrorClass is a short stable identifier for the proximate failure category
	// (e.g., "boot-timeout", "ssh-timeout", "provision-failed"). Empty for non-error rows.
	ErrorClass string `json:"errorClass,omitempty" table:"-"`
	// ErrorSummary is a one-sentence human-readable proximate cause, extracted from
	// runner logs. Hidden from table output for width; visible in JSON.
	ErrorSummary string `json:"errorSummary,omitempty" table:"-"`
}

// Test status values, drawn from tmt's vocabulary. Pytest's pass/fail/error/skip values
// already match; the cross-runner subset is small and stable.
const (
	TestStatusPass  = "pass"
	TestStatusFail  = "fail"
	TestStatusError = "error"
	TestStatusInfo  = "info"
	TestStatusWarn  = "warn"
	TestStatusSkip  = "skip"
)
