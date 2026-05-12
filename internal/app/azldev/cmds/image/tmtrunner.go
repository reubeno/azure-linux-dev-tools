// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package image

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/workdir"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/tmt"
)

const (
	// tmtBaseDirName is the project-work-dir-relative directory under which the
	// runner's source-clone and venv caches live (a sibling of `_global/<date>/...`,
	// the per-run dir tree managed by [workdir.Factory]).
	tmtBaseDirName = "tmt"

	// tmtRunDirLabel is the prefix passed to [workdir.Factory.Create] for tmt run
	// dirs; the factory appends a random suffix to ensure uniqueness.
	tmtRunDirLabel = "tmt"

	// cleanupTimeout bounds the total wall time the deferred fallback cleanup path
	// gets after [tmt.Runner.Run] returns. Cleanup runs on a fresh context so it
	// survives parent-context cancellation (Ctrl-C).
	cleanupTimeout = 2 * time.Minute
)

// RunTmtSuite is the [TestTypeTmt] entry point invoked by the suite-dispatch in
// [runTestSuite]. It bridges azldev's project configuration to the [tmt] package's
// builder API:
//
//   - Allocate a per-run dir via the standard [workdir.Factory].
//   - Run host preflight (executables + dev-headers tmt's pip extras need).
//   - Build a [tmt.RunSpec] from the suite config.
//   - Construct a [tmt.Runner], run the suite.
//   - Map the [tmt.Result] back to [ImageTestResult] rows.
//   - Persist azldev-format sidecars (azldev-results.json, azldev-run.json,
//     host-env.txt) and emit a "to investigate" pointer block on failure.
//   - Defer a fallback `tmt clean` for the residual leak case where tmt's own
//     `cleanup` step did not run (e.g., SIGKILL).
func RunTmtSuite(
	env *azldev.Env, suiteConfig *projectconfig.TestSuiteConfig,
	_ *projectconfig.ImageConfig, options *ImageTestOptions,
) ([]ImageTestResult, error) {
	tmtConfig := suiteConfig.Tmt
	if tmtConfig == nil {
		return nil, fmt.Errorf("test suite %#q is missing tmt configuration", suiteConfig.Name)
	}

	startedAt := time.Now().UTC()

	slog.Info("Running tmt test suite",
		slog.String("suite", suiteConfig.Name),
		slog.String("source-url", tmtConfig.Source.GitURL),
		slog.String("source-ref", tmtConfig.Source.Ref),
		slog.String("plan", tmtConfig.Plan),
		slog.String("image-path", options.ImagePath),
	)

	if err := tmtPreflight(env, tmtConfig); err != nil {
		return nil, err
	}

	runDir, err := allocateTmtRunDir(env)
	if err != nil {
		return nil, err
	}

	slog.Info("tmt run directory",
		slog.String("suite", suiteConfig.Name),
		slog.String("path", runDir))

	if hostEnvErr := writeHostEnvFile(env, runDir); hostEnvErr != nil {
		slog.Warn("Failed to capture host environment for diagnostics",
			slog.String("suite", suiteConfig.Name),
			slog.String("err", hostEnvErr.Error()))
	}

	junitOutPath, err := decideJUnitOutPath(options, runDir)
	if err != nil {
		return nil, err
	}

	spec := buildRunSpec(tmtConfig, junitOutPath)
	runner := tmt.NewRunner(env, tmtBaseDir(env)).
		WithImage(options.ImagePath).
		WithVerbose(env.Verbose())

	// runFinishedSuccessfully tracks whether the run completed normally. The deferred
	// fallback cleanup uses it (rather than the run error) so panics anywhere between
	// here and the end of the function still trigger guest cleanup.
	runFinishedSuccessfully := false

	defer func() {
		if runFinishedSuccessfully {
			return
		}

		// Fresh context so cleanup survives a cancelled parent context (Ctrl-C).
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cleanupCancel()

		venvDir := runner.VenvDirForSpec(spec)

		slog.Info("Running fallback tmt clean for failed run",
			slog.String("suite", suiteConfig.Name),
			slog.String("run-dir", runDir))

		if cleanErr := runner.Clean(cleanupCtx, runDir, venvDir); cleanErr != nil {
			slog.Warn("Fallback tmt clean returned an error",
				slog.String("suite", suiteConfig.Name),
				slog.String("err", cleanErr.Error()))
		}
	}()

	result, runErr := runner.Run(env, runDir, spec)

	rows := mapTmtResultToRows(suiteConfig.Name, runDir, result, runErr)

	publishJUnitIfRequested(env, junitOutPath, options.JUnitXMLPath, suiteConfig.Name)

	persistRunArtifacts(runDir, suiteConfig.Name, options.ImagePath, tmtConfig, result, runErr, startedAt)

	if runErr != nil {
		printFailureDiagnostics(runDir, suiteConfig.Name, tmtConfig.Plan, result)

		return rows, fmt.Errorf("tmt run failed (run dir: %s):\n%w", runDir, runErr)
	}

	slog.Info("tmt suite passed",
		slog.String("suite", suiteConfig.Name),
		slog.String("run-dir", runDir))

	runFinishedSuccessfully = true

	return rows, nil
}

// tmtBaseDir returns the directory under which the tmt package caches source clones
// and per-suite venvs. Lives as a sibling of the `_global/<date>/...` per-run dir
// tree, so caches survive across runs and per-run state is naturally garbage-collected.
func tmtBaseDir(env *azldev.Env) string {
	return filepath.Join(env.WorkDir(), tmtBaseDirName)
}

// allocateTmtRunDir uses azldev's standard [workdir.Factory] to construct a unique,
// timestamped per-run directory under the project's work directory. tmt receives this
// path via `tmt run -i <run-dir>` and writes its run artifacts into it.
//
// The runner additionally passes the run dir's parent as `--workdir-root` to tmt so
// that testcloud's per-VM state (which would otherwise default to `/var/tmp/tmt/...`)
// lands as a sibling under our work-dir tree.
func allocateTmtRunDir(env *azldev.Env) (string, error) {
	factory, err := workdir.NewFactory(env.FS(), env.WorkDir(), env.ConstructionTime())
	if err != nil {
		return "", fmt.Errorf("failed to create work dir factory:\n%w", err)
	}

	runDir, err := factory.Create("", tmtRunDirLabel)
	if err != nil {
		return "", fmt.Errorf("failed to allocate tmt run dir:\n%w", err)
	}

	return runDir, nil
}

// decideJUnitOutPath returns the in-run-dir junit path when --junit-xml is requested
// and the invocation is single-suite, "" otherwise. Multi-suite junit is rejected
// (multi-suite merge isn't implemented yet).
func decideJUnitOutPath(options *ImageTestOptions, runDir string) (string, error) {
	if options.JUnitXMLPath == "" {
		return "", nil
	}

	if len(options.TestSuites) > 1 {
		return "", fmt.Errorf(
			"--junit-xml is only supported when a single test suite is selected (got %d); "+
				"multi-suite junit merging is not implemented yet",
			len(options.TestSuites),
		)
	}

	return filepath.Join(runDir, tmt.JUnitFileName), nil
}

// buildRunSpec translates a [projectconfig.TmtConfig] into the
// shape [tmt.Runner.Run] consumes.
func buildRunSpec(cfg *projectconfig.TmtConfig, junitOutPath string) tmt.RunSpec {
	return tmt.RunSpec{
		Source: tmt.Source{
			GitURL: cfg.Source.GitURL,
			Ref:    cfg.Source.Ref,
		},
		Plan:             cfg.Plan,
		Context:          cfg.Context,
		PipExtras:        cfg.PipExtras,
		RunExtraArgs:     cfg.RunExtraArgs,
		PlanExtraArgs:    cfg.PlanExtraArgs,
		CloudInitRuncmds: cfg.CloudInitRuncmds,
		Provision: tmt.ProvisionSpec{
			How:       tmt.ProvisionHow(cfg.Provision.How),
			ExtraArgs: cfg.Provision.ExtraArgs,
		},
		JUnitOutPath: junitOutPath,
	}
}

// mapTmtResultToRows converts a [tmt.Result] (and the run error, if any) into the
// caller-facing [ImageTestResult] rows that azldev's typed-output system formats.
//
// On success: one row per per-test entry from results.yaml.
// On failure with no per-test entries: one suite-level rollup row with status=error.
// On failure with per-test entries: per-test rows (the failure was in a later step
// like report or finish; tests already ran) — no rollup.
func mapTmtResultToRows(
	suiteName, runDir string, result *tmt.Result, runErr error,
) []ImageTestResult {
	if result == nil {
		// Defensive: shouldn't happen because [tmt.Runner.Run] always returns a
		// Result, but if it ever does, surface a placeholder row so the user sees
		// the failure in the table.
		if runErr == nil {
			return nil
		}

		return []ImageTestResult{{
			Suite:  suiteName,
			Type:   string(projectconfig.TestTypeTmt),
			Status: TestStatusError,
			RunDir: runDir,
		}}
	}

	rows := make([]ImageTestResult, 0, len(result.Tests))
	for _, entry := range result.Tests {
		rows = append(rows, ImageTestResult{
			Suite:           suiteName,
			Type:            string(projectconfig.TestTypeTmt),
			Test:            entry.Name,
			Status:          entry.Status,
			DurationSeconds: entry.Duration.Seconds(),
			OutputPath:      entry.OutputPath,
			RunDir:          result.RunDir,
		})
	}

	if len(rows) == 0 && runErr != nil {
		rows = append(rows, ImageTestResult{
			Suite:        suiteName,
			Type:         string(projectconfig.TestTypeTmt),
			Status:       TestStatusError,
			RunDir:       result.RunDir,
			ErrorClass:   result.ErrorClass,
			ErrorSummary: result.ErrorSummary,
		})
	}

	return rows
}

// persistRunArtifacts writes the azldev-format sidecars (per-test JSON results, per-run
// metadata) into the run dir. Best-effort: errors here are logged but never override
// the original run error.
func persistRunArtifacts(
	runDir, suiteName, imagePath string,
	tmtConfig *projectconfig.TmtConfig,
	result *tmt.Result, runErr error, startedAt time.Time,
) {
	if result != nil && len(result.Tests) > 0 {
		if err := writeAzldevResultsFile(runDir, result.Tests); err != nil {
			slog.Warn("Failed to write azldev-results.json",
				slog.String("suite", suiteName),
				slog.String("err", err.Error()))
		}
	}

	finalStatus := TestStatusPass

	var (
		errorClass   string
		errorSummary string
		vmName       string
	)

	if result != nil {
		errorClass = result.ErrorClass
		errorSummary = result.ErrorSummary
		vmName = result.VMName
	}

	if runErr != nil {
		finalStatus = TestStatusError
	}

	meta := tmtRunMetadata{
		RunID:        filepath.Base(runDir),
		Suite:        suiteName,
		ImagePath:    imagePath,
		Plan:         tmtConfig.Plan,
		ProvisionHow: string(tmtConfig.Provision.How),
		VMName:       vmName,
		StartedAt:    startedAt.Format(time.RFC3339),
		FinishedAt:   time.Now().UTC().Format(time.RFC3339),
		FinalStatus:  finalStatus,
		ErrorClass:   errorClass,
		ErrorSummary: errorSummary,
	}

	if err := writeRunMetadata(runDir, meta); err != nil {
		slog.Warn("Failed to write azldev-run.json",
			slog.String("suite", suiteName),
			slog.String("err", err.Error()))
	}
}

// publishJUnitIfRequested copies tmt's junit.xml output (which lands at
// junitOutPath inside the run dir per [tmt.RunSpec.JUnitOutPath]) to the user-supplied
// --junit-xml path. No-op when --junit-xml wasn't requested.
func publishJUnitIfRequested(
	env *azldev.Env, junitOutPath, userPath, suiteName string,
) {
	if junitOutPath == "" {
		return
	}

	if err := publishJUnit(env, junitOutPath, userPath); err != nil {
		slog.Warn("Failed to publish JUnit XML",
			slog.String("suite", suiteName),
			slog.String("from", junitOutPath),
			slog.String("to", userPath),
			slog.String("err", err.Error()))
	}
}
