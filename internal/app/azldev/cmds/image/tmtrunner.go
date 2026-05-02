// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/workdir"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/prereqs"
	"gopkg.in/yaml.v3"
)

const (
	tmtDirName       = "tmt"
	tmtSourceDirName = "source"
	tmtVenvDirName   = "venv"
	tmtBinName       = "tmt"
	gitProgram       = "git"
	qemuImgProgram   = "qemu-img"
	qcow2Format      = "qcow2"
	// shortHashLen is the length of the short hash used in source-clone directory names.
	shortHashLen = 16
	// azldevResultsFileName is the file emitted by the runner with structured per-test results.
	azldevResultsFileName = "azldev-results.json"
	// azldevRunFileName is the file emitted by the runner with per-run metadata
	// (suite name, VM name, tmt version, timestamps, final status, error class).
	azldevRunFileName = "azldev-run.json"
	// hostEnvFileName captures host-side facts (OS, kernel, virsh/qemu/tmt versions, etc.)
	// at the start of each run so failure reports include them without further digging.
	hostEnvFileName = "host-env.txt"
	// tmtOutputFileName persists tmt's combined stdout+stderr in the run dir so the
	// pretty-printed output remains available after the terminal closes.
	tmtOutputFileName = "tmt-output.log"
	// tmtJUnitFileName is the file written by tmt's junit reporter inside the run dir.
	tmtJUnitFileName = "junit.xml"
	// tmtBootTimeoutSeconds is the value we pass to testcloud (via the TMT_BOOT_TIMEOUT
	// env var) for how long to wait for a guest to come up before giving up. testcloud's
	// own default is 120 seconds; we extend it to give slower hosts and image variants
	// (e.g., images with substantial first-boot work) a fair chance.
	tmtBootTimeoutSeconds = 300
	// resultsFileMode is the permission used when writing the structured results JSON.
	resultsFileMode = 0o600
	// cleanupTimeoutSeconds bounds the total wall time for the deferred guest-cleanup
	// path. Cleanup runs in a *fresh* context so it survives parent-context cancellation
	// (Ctrl-C), which is the root cause we observed of leaked qemu processes.
	cleanupTimeoutSeconds = 120
)

// tmtCancelGracePeriod is how long we let the tmt subprocess unwind after we forward
// a cancellation (SIGINT) to it before Go promotes the cancel to a SIGKILL. Long enough
// for tmt's try/finally to run its `cleanup` step (which tears down provisioned guests
// via guest.stop() + guest.remove()), which can take 30+ seconds when libvirt is slow.
//
//nolint:gochecknoglobals // duration constants need a var; effectively const.
var tmtCancelGracePeriod = 60 * time.Second

// pipExtraForProvisionHow maps each supported tmt provision plugin to the pip extra that
// must be installed alongside `tmt` for that plugin to load.
//
//nolint:gochecknoglobals // Effectively a constant; Go has no const maps.
var pipExtraForProvisionHow = map[projectconfig.TmtProvisionHow]string{
	projectconfig.TmtProvisionHowVirtual: "provision-virtual",
}

// RunTmtSuite runs a tmt-based test suite by cloning the configured source repo at the
// pinned ref, creating a per-suite Python venv, installing tmt with the required extras,
// optionally converting the image-under-test to qcow2, and invoking tmt against the
// configured plan with `provision -h <how> --image <image>`. Structured results are
// extracted to <run-dir>/azldev-results.json and returned as a slice of
// [ImageTestResult] for the caller to format.
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

	prep, err := prepareTmtRun(env, suiteConfig, tmtConfig, options)
	if err != nil {
		return nil, err
	}

	slog.Info("tmt run directory",
		slog.String("suite", suiteConfig.Name),
		slog.String("path", prep.runDir))

	// Best-effort host-env dump so failure reports include OS / virsh / qemu / tmt versions.
	if hostEnvErr := writeHostEnvFile(env, prep); hostEnvErr != nil {
		slog.Warn("Failed to capture host environment for diagnostics",
			slog.String("suite", suiteConfig.Name),
			slog.String("err", hostEnvErr.Error()))
	}

	// runFinishedSuccessfully tracks whether the run reached normal completion. The
	// deferred cleanup uses it (rather than the runErr value) so that panics anywhere
	// between here and the end of the function still trigger guest cleanup. On any
	// non-success exit (error, panic, ctx-cancel) we invoke testcloud's own cleanup to
	// work around the upstream tmt bug where boot-timeout failures leak libvirt guests
	// (provision/testcloud.py:1318 only stops guests whose primary_address was set,
	// which never happens on boot failure).
	runFinishedSuccessfully := false

	defer func() {
		if runFinishedSuccessfully {
			return
		}

		// Use a fresh context for cleanup, independent of the (possibly cancelled)
		// parent context. Without this, Ctrl-C / parent-timeout would propagate to
		// every subprocess in the cleanup path and leak the very VMs we're trying to
		// reclaim.
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(), cleanupTimeoutSeconds*time.Second)
		defer cleanupCancel()

		cleanTmtRunGuests(cleanupCtx, env, prep)
	}()

	runErr := executeTmt(env, prep)

	results := postProcessTmtRun(env, prep, options, suiteConfig, runErr, startedAt)
	if runErr != nil {
		return results, fmt.Errorf("tmt run failed (run dir: %s):\n%w", prep.runDir, runErr)
	}

	runFinishedSuccessfully = true

	return results, nil
}

// tmtRunPrep groups the resolved paths and decisions made before launching tmt.
type tmtRunPrep struct {
	tmtConfig    *projectconfig.TmtConfig
	suiteName    string
	venvDir      string
	sourceDir    string
	runDir       string
	imagePath    string
	junitOutPath string
}

// prepareTmtRun performs all setup steps that must succeed before invoking tmt: source
// clone, venv install, run-dir allocation, image conversion, and JUnit decision.
func prepareTmtRun(
	env *azldev.Env, suiteConfig *projectconfig.TestSuiteConfig,
	tmtConfig *projectconfig.TmtConfig, options *ImageTestOptions,
) (*tmtRunPrep, error) {
	workDir := env.WorkDir()
	if workDir == "" {
		return nil, fmt.Errorf(
			"cannot run tmt suite %#q: project work directory is not configured",
			suiteConfig.Name,
		)
	}

	tmtBaseDir := filepath.Join(workDir, tmtDirName)

	sourceDir, err := ensureTmtSourceClone(env, tmtBaseDir, &tmtConfig.Source)
	if err != nil {
		return nil, fmt.Errorf("failed to set up tmt source repo:\n%w", err)
	}

	venvDir, err := ensureTmtVenv(env, suiteConfig.Name, tmtConfig)
	if err != nil {
		return nil, err
	}

	runDir, err := allocateTmtRunDir(env, suiteConfig.Name)
	if err != nil {
		return nil, err
	}

	imagePath, err := ensureQcow2Image(env, options.ImagePath, runDir)
	if err != nil {
		return nil, err
	}

	junitOutPath, err := decideJUnitOutPath(options, runDir)
	if err != nil {
		return nil, err
	}

	return &tmtRunPrep{
		tmtConfig:    tmtConfig,
		suiteName:    suiteConfig.Name,
		venvDir:      venvDir,
		sourceDir:    sourceDir,
		runDir:       runDir,
		imagePath:    imagePath,
		junitOutPath: junitOutPath,
	}, nil
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

	return filepath.Join(runDir, tmtJUnitFileName), nil
}

// executeTmt builds and invokes the tmt argv against the prepared run. tmt's combined
// stdout+stderr is *teed* to <run-dir>/tmt-output.log so the pretty-printed output
// remains diagnosable after the user's terminal disconnects.
func executeTmt(env *azldev.Env, prep *tmtRunPrep) error {
	tmtBin := filepath.Join(prep.venvDir, "bin", tmtBinName)
	argv := buildTmtArgv(env, prep.tmtConfig, prep.runDir, prep.imagePath, prep.junitOutPath)

	slog.Info("Running tmt",
		slog.String("suite", prep.suiteName),
		slog.String("bin", tmtBin),
		slog.Any("args", argv))

	outputLogPath := filepath.Join(prep.runDir, tmtOutputFileName)

	outputFile, err := os.OpenFile(outputLogPath,
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC, resultsFileMode)
	if err != nil {
		return fmt.Errorf("failed to open tmt output log %#q:\n%w", outputLogPath, err)
	}

	defer func() { _ = outputFile.Close() }()

	tmtCmd := exec.CommandContext(env, tmtBin, argv...)
	tmtCmd.Stdout = io.MultiWriter(os.Stdout, outputFile)
	tmtCmd.Stderr = io.MultiWriter(os.Stderr, outputFile)
	tmtCmd.Dir = prep.sourceDir

	subprocessEnv, err := buildTmtSubprocessEnv(prep)
	if err != nil {
		return fmt.Errorf("failed to build tmt subprocess env:\n%w", err)
	}

	tmtCmd.Env = subprocessEnv

	// On context cancellation (Ctrl-C, parent SIGTERM/SIGHUP), send SIGINT so tmt has
	// a chance to run its own try/finally cleanup (which calls guest.stop() +
	// guest.remove()). Without this, exec.CommandContext defaults to SIGKILL, which
	// terminates tmt instantly with no chance to unwind — leaking libvirt guests.
	// WaitDelay imposes an upper bound: if tmt hasn't exited within the grace window,
	// Go promotes the cancel to a SIGKILL.
	tmtCmd.Cancel = func() error {
		slog.Warn("Forwarding cancellation to tmt subprocess; allowing time for graceful unwind",
			slog.String("suite", prep.suiteName),
			slog.Duration("wait-delay", tmtCancelGracePeriod))

		return tmtCmd.Process.Signal(os.Interrupt)
	}
	tmtCmd.WaitDelay = tmtCancelGracePeriod

	wrapped, err := env.Command(tmtCmd)
	if err != nil {
		return fmt.Errorf("failed to create tmt command:\n%w", err)
	}

	if err := wrapped.Run(env); err != nil {
		return fmt.Errorf("tmt invocation failed:\n%w", err)
	}

	return nil
}

// tmtSubprocessEnv returns the environment to pass to tmt. We start from the inherited
// process environment so user-managed knobs (HOME, PATH, virsh creds, etc.) flow through,
// then add azldev-managed overrides — currently just an extended TMT_BOOT_TIMEOUT.
//
// Deprecated: use [buildTmtSubprocessEnv] when you have a [tmtRunPrep] in scope; it
// additionally generates the TMT_PLUGINS pointer and is the right entry point for
// the per-run tmt invocation. This bare helper remains for the fallback cleanup
// path (`tmt clean guests`), which doesn't need plugin injection.
func tmtSubprocessEnv() []string {
	env := os.Environ()
	env = append(env, fmt.Sprintf("TMT_BOOT_TIMEOUT=%d", tmtBootTimeoutSeconds))

	return env
}

// buildTmtSubprocessEnv extends [tmtSubprocessEnv] with a TMT_PLUGINS pointer when the
// suite config has [TmtConfig.CloudInitRuncmds] entries. It writes a tiny Python file
// into the run dir that, on tmt startup, appends the configured commands to
// `tmt.steps.provision.testcloud.TESTCLOUD_WORKAROUNDS`. tmt then funnels them into
// cloud-init's runcmd, where they execute on the guest before testcloud.service starts
// (and therefore before testcloud's boot-complete probe fires).
//
// The plugin file is regenerated for every run. We deliberately do *not* cache or share
// it across suites — each suite's runcmds are independent, and a plugin from a stale
// run shouldn't leak into a fresh one.
func buildTmtSubprocessEnv(prep *tmtRunPrep) ([]string, error) {
	env := tmtSubprocessEnv()

	if len(prep.tmtConfig.CloudInitRuncmds) == 0 {
		return env, nil
	}

	pluginDir, err := writeCloudInitRuncmdsPlugin(prep.runDir, prep.tmtConfig.CloudInitRuncmds)
	if err != nil {
		return nil, err
	}

	slog.Info("Injecting cloud-init runcmds into tmt provision via TMT_PLUGINS",
		slog.String("suite", prep.suiteName),
		slog.String("plugin-dir", pluginDir),
		slog.Int("runcmd-count", len(prep.tmtConfig.CloudInitRuncmds)))

	env = append(env, "TMT_PLUGINS="+pluginDir)

	return env, nil
}

// writeCloudInitRuncmdsPlugin emits a Python plugin file under <runDir>/tmt-plugins/
// that appends the configured runcmd entries to tmt's TESTCLOUD_WORKAROUNDS list at
// import time. Returns the directory path suitable for $TMT_PLUGINS.
//
// We use Python's repr() (via %q with []string here, written as a Python list literal
// with json-style strings) to ensure our user-supplied strings can't escape into the
// Python source as code. The Python file imports tmt's module, then for each command
// calls list.append() — the simplest possible form, no class, no decorator.
func writeCloudInitRuncmdsPlugin(runDir string, runcmds []string) (string, error) {
	pluginDir := filepath.Join(runDir, "tmt-plugins")
	if err := os.MkdirAll(pluginDir, defaultDirMode); err != nil {
		return "", fmt.Errorf("failed to create tmt plugin dir %#q:\n%w", pluginDir, err)
	}

	body, err := renderCloudInitRuncmdsPlugin(runcmds)
	if err != nil {
		return "", fmt.Errorf("failed to render tmt plugin:\n%w", err)
	}

	pluginFile := filepath.Join(pluginDir, "azldev_cloud_init_runcmds.py")
	if err := os.WriteFile(pluginFile, []byte(body), resultsFileMode); err != nil {
		return "", fmt.Errorf("failed to write tmt plugin %#q:\n%w", pluginFile, err)
	}

	return pluginDir, nil
}

// renderCloudInitRuncmdsPlugin returns the Python source for the TMT_PLUGINS file. The
// commands are JSON-encoded so they round-trip safely as Python string literals (JSON
// strings are a subset of Python's). This avoids any code-injection risk from user
// configuration.
func renderCloudInitRuncmdsPlugin(runcmds []string) (string, error) {
	jsonBytes, err := json.Marshal(runcmds)
	if err != nil {
		return "", fmt.Errorf("failed to marshal runcmds:\n%w", err)
	}

	return fmt.Sprintf(`# Generated by azldev. Do not edit.
#
# This file is loaded by tmt at startup via the TMT_PLUGINS environment variable
# (see tmt/plugins/__init__.py:_explore_custom_directories). Module-level code runs
# before any tmt step, so by the time the provision step builds its cloud-init data,
# our entries are already in the TESTCLOUD_WORKAROUNDS list.
#
# Each entry is a shell command that runs during cloud-init runcmd on the guest,
# *before* testcloud.service starts the boot-complete HTTP probe.

import tmt.steps.provision.testcloud as _tc

_AZLDEV_RUNCMDS = %s

for _cmd in _AZLDEV_RUNCMDS:
    _tc.TESTCLOUD_WORKAROUNDS.append(_cmd)
`, string(jsonBytes)), nil
}

const defaultDirMode = 0o755

// postProcessTmtRun runs after tmt exits. It extracts structured results, publishes
// JUnit, classifies the failure (when there is one), copies the volatile console log
// into the run dir, writes a per-run metadata file, and emits a "to investigate" block
// of file pointers so the user has a single place to start diagnosing without grepping
// the host. Best-effort throughout: errors here are logged but never override the
// original tmt error.
//
// Guest cleanup on failure is handled by a defer in [RunTmtSuite] (so it survives
// panics); this function is purely concerned with results, durability, and diagnostics.
func postProcessTmtRun(
	env *azldev.Env, prep *tmtRunPrep, options *ImageTestOptions,
	suiteConfig *projectconfig.TestSuiteConfig, runErr error,
	startedAt time.Time,
) []ImageTestResult {
	suiteName := suiteConfig.Name

	tmtResults := extractAndPersistResults(env, prep, suiteName)
	publishJUnitIfRequested(env, prep, options, suiteName)

	// Build the result rows.
	rows := make([]ImageTestResult, 0, len(tmtResults))
	for _, entry := range tmtResults {
		rows = append(rows, ImageTestResult{
			Suite:           suiteName,
			Type:            string(projectconfig.TestTypeTmt),
			Test:            entry.name,
			Status:          entry.status,
			DurationSeconds: entry.durationSeconds,
			OutputPath:      entry.outputPath,
			RunDir:          prep.runDir,
		})
	}

	// Classify the failure (when there is one) so we can surface a clean summary line
	// and embed it in the per-run metadata.
	var (
		errorClass   string
		errorSummary string
	)

	if runErr != nil {
		errorClass, errorSummary = classifyTmtFailure(prep.runDir, runErr)
	}

	// If tmt failed before any test produced a result, emit a suite-level rollup so the
	// failure shows up in the caller's table / JSON output instead of being invisible.
	if len(rows) == 0 && runErr != nil {
		rows = append(rows, ImageTestResult{
			Suite:        suiteName,
			Type:         string(projectconfig.TestTypeTmt),
			Status:       TestStatusError,
			RunDir:       prep.runDir,
			ErrorClass:   errorClass,
			ErrorSummary: errorSummary,
		})
	}

	finalizeRun(env, prep, options, suiteName, runErr, errorClass, errorSummary, startedAt)

	return rows
}

// cleanTmtRunGuests is a *fallback* cleanup path invoked from a defer when a tmt run
// did not finish normally. In the common case tmt itself takes care of cleanup: with
// the `--all` flag in the argv (see [buildTmtArgv]), tmt's own `cleanup` step runs in
// the `try/finally` of [tmt.base.plan.Plan.go] even when an earlier step fails, and the
// cleanup step calls `guest.stop()` + `guest.remove()` on every guest tmt provisioned.
//
// This function exists for the residual cases where tmt's own cleanup did not run:
//
//   - the tmt subprocess was killed (SIGKILL, OOM) before it could reach the finally;
//   - the tmt subprocess crashed without unwinding (e.g., a Python segfault);
//   - the parent run was aborted by something that bypassed Go's defers in azldev itself.
//
// We invoke `tmt clean -i <run-dir>` — tmt's own scoped-cleanup CLI. With cleanup having
// run in-band on most paths, this is usually a no-op. Where tmt's in-band cleanup didn't
// run, the persisted guests.yaml under <run-dir> still describes the guests tmt
// provisioned, and `tmt clean` is the official way to tear them down.
//
// Cleanup runs on a *fresh* context provided by the caller — the run's parent context
// may already be cancelled (Ctrl-C, parent timeout), and inheriting that would kill
// the cleanup subprocess immediately, defeating the point.
func cleanTmtRunGuests(ctx context.Context, env *azldev.Env, prep *tmtRunPrep) {
	slog.Info("Running fallback tmt clean for failed run",
		slog.String("suite", prep.suiteName),
		slog.String("run-dir", prep.runDir),
	)

	if err := runTmtCleanGuests(ctx, env, prep); err != nil {
		slog.Warn("Fallback tmt clean returned an error",
			slog.String("suite", prep.suiteName),
			slog.String("run-dir", prep.runDir),
			slog.String("err", err.Error()))
	}
}

// runTmtCleanGuests invokes `tmt clean guests -i <run-dir>` against the per-suite venv's
// tmt binary, scoped to the given run directory.
func runTmtCleanGuests(ctx context.Context, env *azldev.Env, prep *tmtRunPrep) error {
	tmtBin := filepath.Join(prep.venvDir, "bin", tmtBinName)

	cleanCmd := exec.CommandContext(ctx, tmtBin, "clean", "guests", "-i", prep.runDir)
	cleanCmd.Stdout = os.Stdout
	cleanCmd.Stderr = os.Stderr
	cleanCmd.Env = tmtSubprocessEnv()

	wrapped, err := env.Command(cleanCmd)
	if err != nil {
		return fmt.Errorf("failed to create tmt clean command:\n%w", err)
	}

	if err := wrapped.Run(ctx); err != nil {
		return fmt.Errorf("tmt clean guests failed:\n%w", err)
	}

	return nil
}

// tmtPreflight verifies the host has the binaries, headers, and runtime access tmt will
// need to install and run. Header-providing packages (e.g., libvirt-devel, python3-devel)
// are required because tmt's pip extras (notably provision-virtual via libvirt-python)
// build native modules from source — there are no upstream wheels.
func tmtPreflight(env *azldev.Env, tmtConfig *projectconfig.TmtConfig) error {
	for _, exec := range tmtBaseExecutables() {
		if err := prereqs.RequireExecutable(env, exec.name, exec.prereq); err != nil {
			return fmt.Errorf("%s is required to run tmt tests:\n%w", exec.label, err)
		}
	}

	for _, hdr := range tmtBuildHeaders(env) {
		if err := prereqs.RequireFile(env, hdr.label, hdr.path, hdr.prereq); err != nil {
			return fmt.Errorf("%s is required to build tmt's native dependencies:\n%w", hdr.label, err)
		}
	}

	// Provision-specific preflight.
	if tmtConfig.Provision.How == projectconfig.TmtProvisionHowVirtual {
		for _, exec := range tmtVirtualExecutables() {
			if err := prereqs.RequireExecutable(env, exec.name, exec.prereq); err != nil {
				return fmt.Errorf("%s is required for tmt provision -h virtual:\n%w", exec.label, err)
			}
		}

		for _, hdr := range tmtVirtualHeaders() {
			if err := prereqs.RequireFile(env, hdr.label, hdr.path, hdr.prereq); err != nil {
				return fmt.Errorf("%s is required for tmt provision -h virtual:\n%w", hdr.label, err)
			}
		}

		// Don't fail loudly here — libvirt access is sometimes unavailable in CI but
		// tmt's own error will be more informative. Just log a warning if neither
		// session nor system libvirt seems reachable.
		if !libvirtReachable(env) {
			slog.Warn("Could not contact libvirt (session or system); " +
				"tmt provision -h virtual may fail. Ensure libvirtd is running and your user has access.")
		}
	}

	return nil
}

// tmtPrereqExec describes an executable prerequisite, with the package(s) that provide it.
type tmtPrereqExec struct {
	name   string                 // executable name as found on PATH
	label  string                 // human label for messages
	prereq *prereqs.PackagePrereq // nil = don't auto-install
}

// tmtPrereqFile describes a file prerequisite (typically a header or pkg-config file
// installed by a `*-devel` package).
type tmtPrereqFile struct {
	path   string
	label  string
	prereq *prereqs.PackagePrereq
}

// tmtBaseExecutables returns prereqs that are common to every tmt run (regardless of
// provision how).
func tmtBaseExecutables() []tmtPrereqExec {
	return []tmtPrereqExec{
		{name: pythonProgram, label: "python3", prereq: &prereqs.PackagePrereq{
			AzureLinuxPackages: []string{"python3"},
			FedoraPackages:     []string{"python3"},
		}},
		{name: gitProgram, label: "git", prereq: &prereqs.PackagePrereq{
			AzureLinuxPackages: []string{"git"},
			FedoraPackages:     []string{"git"},
		}},
		{name: qemuImgProgram, label: "qemu-img", prereq: &prereqs.PackagePrereq{
			AzureLinuxPackages: []string{"qemu-img"},
			FedoraPackages:     []string{"qemu-img"},
		}},
	}
}

// tmtBuildHeaders returns header prereqs needed to build tmt's native pip dependencies
// (independent of provision how — Python.h is needed by anything that builds C
// extensions). The Python include path is discovered from the running interpreter.
func tmtBuildHeaders(env *azldev.Env) []tmtPrereqFile {
	out := []tmtPrereqFile{
		{
			path:  "/usr/bin/gcc",
			label: "C compiler (gcc)",
			prereq: &prereqs.PackagePrereq{
				AzureLinuxPackages: []string{"gcc"},
				FedoraPackages:     []string{"gcc"},
			},
		},
		{
			path:  "/usr/bin/pkg-config",
			label: "pkg-config",
			prereq: &prereqs.PackagePrereq{
				AzureLinuxPackages: []string{"pkgconf-pkg-config"},
				FedoraPackages:     []string{"pkgconf-pkg-config"},
			},
		},
	}

	if hdr := pythonDevelHeader(env); hdr != "" {
		out = append(out, tmtPrereqFile{
			path:  hdr,
			label: "Python development headers (python3-devel)",
			prereq: &prereqs.PackagePrereq{
				AzureLinuxPackages: []string{"python3-devel"},
				FedoraPackages:     []string{"python3-devel"},
			},
		})
	}

	return out
}

// tmtVirtualExecutables returns executable prereqs specific to the `virtual` provisioner.
func tmtVirtualExecutables() []tmtPrereqExec {
	return []tmtPrereqExec{
		{name: "virsh", label: "virsh (libvirt client)", prereq: &prereqs.PackagePrereq{
			AzureLinuxPackages: []string{"libvirt-client"},
			FedoraPackages:     []string{"libvirt-client"},
		}},
		// qemu-system-x86_64 covers the vast majority of cases on x86_64 hosts. ARM hosts
		// will need qemu-system-aarch64 and we can extend later as needed.
		{name: "qemu-system-x86_64", label: "qemu-system-x86_64", prereq: &prereqs.PackagePrereq{
			AzureLinuxPackages: []string{"qemu-kvm"},
			FedoraPackages:     []string{"qemu-system-x86"},
		}},
	}
}

// tmtVirtualHeaders returns header prereqs specific to the `virtual` provisioner —
// principally libvirt's pkg-config + headers needed by libvirt-python's source build.
func tmtVirtualHeaders() []tmtPrereqFile {
	return []tmtPrereqFile{
		{
			path:  "/usr/lib64/pkgconfig/libvirt.pc",
			label: "libvirt development files (libvirt-devel)",
			prereq: &prereqs.PackagePrereq{
				AzureLinuxPackages: []string{"libvirt-devel"},
				FedoraPackages:     []string{"libvirt-devel"},
			},
		},
	}
}

// pythonDevelHeader returns the absolute path to the Python.h header for the python3
// interpreter on PATH, or "" if it cannot be determined. The path is what we present to
// [prereqs.RequireFile]; if the interpreter can't be queried (very early failure),
// caller skips the check and relies on the build-time error.
func pythonDevelHeader(env *azldev.Env) string {
	pyCmd := exec.CommandContext(env, pythonProgram, "-c",
		`import sysconfig; print(sysconfig.get_path("include"))`)

	var stdout strings.Builder

	pyCmd.Stdout = &stdout
	pyCmd.Stderr = io.Discard

	wrapped, err := env.Command(pyCmd)
	if err != nil {
		return ""
	}

	if err := wrapped.Run(env); err != nil {
		return ""
	}

	includeDir := strings.TrimSpace(stdout.String())
	if includeDir == "" {
		return ""
	}

	return filepath.Join(includeDir, "Python.h")
}

// libvirtReachable returns true if `virsh -c qemu:///session list` or
// `virsh -c qemu:///system list` returns successfully. Best-effort; absence of `virsh`
// or any error returns false (and we log a warning rather than fail).
func libvirtReachable(env *azldev.Env) bool {
	for _, uri := range []string{"qemu:///session", "qemu:///system"} {
		c := exec.CommandContext(env, "virsh", "-c", uri, "list")
		c.Stdout = io.Discard
		c.Stderr = io.Discard

		wrapped, err := env.Command(c)
		if err != nil {
			continue
		}

		if err := wrapped.Run(env); err == nil {
			return true
		}
	}

	return false
}

// ensureTmtSourceClone clones the configured repo at the configured ref into a directory
// keyed by hash(git-url + ref). Idempotent across runs: an already-checked-out repo
// at the right ref is reused.
func ensureTmtSourceClone(
	env *azldev.Env, tmtBaseDir string, source *projectconfig.TmtGitSource,
) (string, error) {
	key := sha256.Sum256([]byte(source.GitURL + "\x00" + source.Ref))
	keyHex := hex.EncodeToString(key[:])[:shortHashLen]
	repoDir := filepath.Join(tmtBaseDir, tmtSourceDirName, keyHex)

	exists, err := fileutils.DirExists(env.FS(), repoDir)
	if err != nil {
		return "", fmt.Errorf("cannot check tmt source dir at %#q:\n%w", repoDir, err)
	}

	if exists {
		slog.Info("Reusing existing tmt source clone",
			slog.String("path", repoDir),
			slog.String("ref", source.Ref))

		return repoDir, nil
	}

	if err := fileutils.MkdirAll(env.FS(), filepath.Dir(repoDir)); err != nil {
		return "", fmt.Errorf("failed to create tmt source parent dir:\n%w", err)
	}

	slog.Info("Cloning tmt source repo",
		slog.String("url", source.GitURL),
		slog.String("ref", source.Ref),
		slog.String("dest", repoDir))

	if err := runGitCommand(env, "", "clone", "--quiet", source.GitURL, repoDir); err != nil {
		return "", fmt.Errorf("git clone of %#q failed:\n%w", source.GitURL, err)
	}

	if err := runGitCommand(env, repoDir, "checkout", "--quiet", source.Ref); err != nil {
		return "", fmt.Errorf("git checkout of %#q failed:\n%w", source.Ref, err)
	}

	return repoDir, nil
}

// runGitCommand runs `git <args...>` with the working directory set to dir (if non-empty).
func runGitCommand(env *azldev.Env, dir string, args ...string) error {
	gitCmd := exec.CommandContext(env, gitProgram, args...)
	gitCmd.Stdout = os.Stdout
	gitCmd.Stderr = os.Stderr

	if dir != "" {
		gitCmd.Dir = dir
	}

	wrapped, err := env.Command(gitCmd)
	if err != nil {
		return fmt.Errorf("failed to create git command:\n%w", err)
	}

	if err := wrapped.Run(env); err != nil {
		return fmt.Errorf("git command failed:\n%w", err)
	}

	return nil
}

// ensureTmtVenv creates (or reuses) a per-suite venv and installs tmt with the union of
// auto-derived extras (from provision.how) and user-supplied extras.
func ensureTmtVenv(
	env *azldev.Env, suiteName string, tmtConfig *projectconfig.TmtConfig,
) (string, error) {
	venvDir := filepath.Join(env.WorkDir(), tmtDirName, tmtVenvDirName, suiteName)
	venvPython := filepath.Join(venvDir, "bin", pythonProgram)

	exists, err := fileutils.Exists(env.FS(), venvPython)
	if err != nil {
		return "", fmt.Errorf("cannot check tmt venv at %#q:\n%w", venvDir, err)
	}

	if !exists {
		if err := createPythonVenv(env, venvDir); err != nil {
			return "", err
		}
	} else {
		slog.Info("Reusing existing tmt venv", slog.String("path", venvDir))
	}

	extras := mergedPipExtras(tmtConfig)

	pipTarget := tmtBinName
	if len(extras) > 0 {
		pipTarget = fmt.Sprintf("%s[%s]", tmtBinName, strings.Join(extras, ","))
	}

	slog.Info("Installing tmt", slog.String("target", pipTarget))

	pipCmd := exec.CommandContext(env, venvPython, "-m", "pip", "install", "--quiet", pipTarget)
	pipCmd.Stdout = os.Stdout
	pipCmd.Stderr = os.Stderr

	wrapped, err := env.Command(pipCmd)
	if err != nil {
		return "", fmt.Errorf("failed to create pip install command:\n%w", err)
	}

	if err := wrapped.Run(env); err != nil {
		return "", fmt.Errorf("failed to install %s:\n%w", pipTarget, err)
	}

	return venvDir, nil
}

// mergedPipExtras returns the union of extras auto-derived from provision.how and the
// user-supplied list, deduplicated and stably ordered.
func mergedPipExtras(tmtConfig *projectconfig.TmtConfig) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(tmtConfig.PipExtras)+1)

	add := func(extra string) {
		if extra == "" {
			return
		}

		if _, ok := seen[extra]; ok {
			return
		}

		seen[extra] = struct{}{}

		out = append(out, extra)
	}

	if extra, ok := pipExtraForProvisionHow[tmtConfig.Provision.How]; ok {
		add(extra)
	}

	for _, extra := range tmtConfig.PipExtras {
		add(extra)
	}

	sort.Strings(out)

	return out
}

// allocateTmtRunDir uses azldev's standard [workdir.Factory] to construct a unique,
// timestamped per-run directory under the project's work directory. Tmt receives this
// path via `tmt run -i <run-dir>` and writes its run artifacts into it. We additionally
// pass the run dir's parent as `--workdir-root` to tmt so that testcloud's per-VM
// state (typically `/var/tmp/tmt/testcloud/`) lands as a sibling under our work-dir
// rather than in /var/tmp.
//
// Layout:
//
//	<azldev-work-dir>/_global/<YYYY-MM-DD.HHMMSS>/tmt-<suite>-XXXXX/   ← run dir
//	<azldev-work-dir>/_global/<YYYY-MM-DD.HHMMSS>/testcloud/          ← tmt --workdir-root
func allocateTmtRunDir(env *azldev.Env, suiteName string) (string, error) {
	factory, err := workdir.NewFactory(env.FS(), env.WorkDir(), env.ConstructionTime())
	if err != nil {
		return "", fmt.Errorf("failed to create work dir factory:\n%w", err)
	}

	runDir, err := factory.Create("", "tmt-"+suiteName)
	if err != nil {
		return "", fmt.Errorf("failed to allocate tmt run dir for suite %#q:\n%w", suiteName, err)
	}

	return runDir, nil
}

// ensureQcow2Image returns a path to a qcow2 image, converting from another format if
// needed. The converted image is placed inside runDir so it is naturally cleaned up
// alongside other run artifacts and never reused across runs.
func ensureQcow2Image(env *azldev.Env, srcImage, runDir string) (string, error) {
	if srcImage == "" {
		return "", errors.New("no image-path was provided to the tmt runner")
	}

	exists, err := fileutils.Exists(env.FS(), srcImage)
	if err != nil {
		return "", fmt.Errorf("cannot check image at %#q:\n%w", srcImage, err)
	}

	if !exists {
		return "", fmt.Errorf("image not found at %#q", srcImage)
	}

	format, err := detectImageFormat(env, srcImage)
	if err != nil {
		return "", err
	}

	if format == qcow2Format {
		slog.Info("Image is already qcow2; using as-is", slog.String("path", srcImage))

		return srcImage, nil
	}

	if err := fileutils.MkdirAll(env.FS(), runDir); err != nil {
		return "", fmt.Errorf("failed to create run dir for image conversion:\n%w", err)
	}

	dst := filepath.Join(runDir, "image.qcow2")

	slog.Info("Converting image to qcow2",
		slog.String("from", srcImage),
		slog.String("from-format", format),
		slog.String("to", dst))

	c := exec.CommandContext(env, qemuImgProgram, "convert", "-O", qcow2Format, srcImage, dst)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	wrapped, err := env.Command(c)
	if err != nil {
		return "", fmt.Errorf("failed to create qemu-img command:\n%w", err)
	}

	if err := wrapped.Run(env); err != nil {
		return "", fmt.Errorf("qemu-img convert failed:\n%w", err)
	}

	return dst, nil
}

// detectImageFormat shells out to `qemu-img info --output=json` and returns the reported
// format string (e.g., "qcow2", "raw", "vpc" for VHD).
func detectImageFormat(env *azldev.Env, path string) (string, error) {
	infoCmd := exec.CommandContext(env, qemuImgProgram, "info", "--output=json", path)

	var stdout strings.Builder

	infoCmd.Stdout = &stdout
	infoCmd.Stderr = os.Stderr

	wrapped, err := env.Command(infoCmd)
	if err != nil {
		return "", fmt.Errorf("failed to create qemu-img info command:\n%w", err)
	}

	if err := wrapped.Run(env); err != nil {
		return "", fmt.Errorf("qemu-img info failed:\n%w", err)
	}

	var info struct {
		Format string `json:"format"`
	}

	if err := json.Unmarshal([]byte(stdout.String()), &info); err != nil {
		return "", fmt.Errorf("failed to parse qemu-img info output:\n%w", err)
	}

	if info.Format == "" {
		return "", fmt.Errorf("qemu-img info did not report a format for %#q", path)
	}

	return info.Format, nil
}

// buildTmtArgv constructs the step-scoped tmt argv:
//
//	tmt [-vv if azldev verbose] [-c k=v ...] <run-extra-args>
//	    run --all --workdir-root <run-dir-parent> -i <run-dir>
//	    plan -n <plan> <plan-extra-args>
//	    provision [-h <how>] --image <image> <provision-extra-args>
//	    [report -h junit --file <junit-out>]
//
// `--all` (`-a`) tells tmt to run every step (discover/provision/prepare/execute/report
// /finish/cleanup) while still letting us customize the provision and report step
// options. This is critical: tmt's `cleanup` step is what calls `guest.stop()` +
// `guest.remove()` to tear down provisioned VMs, and tmt only runs steps that are
// "enabled". Without `--all`, naming `provision` (and optionally `report`) on the
// command line restricts the run to those steps only — `cleanup` never runs and VMs
// leak. With `--all`, tmt's own `try/finally` in plan.go() guarantees the cleanup step
// runs even when an earlier step (e.g., provision/boot-timeout) fails.
//
// `--workdir-root <run-dir-parent>` re-roots all of tmt's auxiliary state — including
// testcloud's per-VM data dir, which would otherwise default to `/var/tmp/tmt/testcloud`
// — into our azldev work-dir tree. Combined with `-i <run-dir>`, this keeps every
// artifact tmt produces inside a single dir hierarchy we manage.
//
// When azldev is in verbose mode and the user has not already supplied a verbosity flag
// in run-extra-args, we automatically pass `-vv` so tmt's own verbose output flows
// through and lands in the run dir's tmt-output.log.
func buildTmtArgv(
	env *azldev.Env, tmtConfig *projectconfig.TmtConfig,
	runDir, imagePath, junitOutPath string,
) []string {
	args := make([]string, 0, 32) //nolint:mnd // small initial capacity hint

	if env != nil && env.Verbose() && !runExtraArgsHaveVerbosity(tmtConfig.RunExtraArgs) {
		args = append(args, "-vv")
	}

	// Pre-run options: -c k=v (multi-valued; emit one per (key, value)).
	keys := make([]string, 0, len(tmtConfig.Context))
	for k := range tmtConfig.Context {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, k := range keys {
		for _, v := range tmtConfig.Context[k] {
			args = append(args, "-c", k+"="+v)
		}
	}

	args = append(args, tmtConfig.RunExtraArgs...)

	// run subcommand. -a makes tmt run all steps (so cleanup runs on failure).
	// --workdir-root re-roots tmt's auxiliary state (incl. testcloud per-VM dirs) into
	// our work-dir so nothing important lands in /var/tmp/tmt.
	args = append(args, "run", "-a", "--workdir-root", filepath.Dir(runDir), "-i", runDir)

	// plan step.
	args = append(args, "plan", "-n", tmtConfig.Plan)
	args = append(args, tmtConfig.PlanExtraArgs...)

	// provision step.
	args = append(args, "provision")

	if tmtConfig.Provision.How != "" {
		args = append(args, "-h", string(tmtConfig.Provision.How))
	}

	args = append(args, "--image", imagePath)
	args = append(args, tmtConfig.ProvisionExtraArgs...)

	// report step (junit), only when requested.
	if junitOutPath != "" {
		args = append(args, "report", "-h", "junit", "--file", junitOutPath)
	}

	return args
}

// tmtResultEntry is the intermediate, unexported representation of a tmt test result
// after parsing results.yaml. The exported [ImageTestResult] is what the caller surfaces
// to the framework; this internal shape keeps tmt-specific decoding contained here.
type tmtResultEntry struct {
	name            string
	status          string
	durationSeconds float64
	outputPath      string
}

// extractTmtResults walks the canonical results.yaml location for the given plan and
// returns the parsed entries. Missing files surface as an error; the caller decides
// whether to treat them as fatal.
func extractTmtResults(env *azldev.Env, runDir, plan string) ([]tmtResultEntry, error) {
	// tmt writes results to <run-dir>/<plan-relative-path>/execute/results.yaml.
	// Plan names start with '/'; the on-disk path strips the leading slash.
	planPath := strings.TrimPrefix(plan, "/")
	resultsPath := filepath.Join(runDir, planPath, "execute", "results.yaml")

	exists, err := fileutils.Exists(env.FS(), resultsPath)
	if err != nil {
		return nil, fmt.Errorf("cannot check results at %#q:\n%w", resultsPath, err)
	}

	if !exists {
		return nil, fmt.Errorf("results.yaml not found at %#q (tmt may have failed before execute)",
			resultsPath)
	}

	raw, err := os.ReadFile(resultsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read %#q:\n%w", resultsPath, err)
	}

	results, err := parseTmtResultsYAML(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse results.yaml:\n%w", err)
	}

	return results, nil
}

// writeAzldevResultsFile emits the structured per-test results next to the run dir as a
// human/script-friendly JSON sidecar. This file is the durable, on-disk form of the same
// data returned through the runner; the in-process [ImageTestResult] flow is what the CLI
// table/JSON output uses.
func writeAzldevResultsFile(runDir string, results []tmtResultEntry) error {
	type fileEntry struct {
		Name            string  `json:"name"`
		Status          string  `json:"status"`
		DurationSeconds float64 `json:"durationSeconds"`
		OutputPath      string  `json:"outputPath,omitempty"`
	}

	entries := make([]fileEntry, 0, len(results))
	for _, result := range results {
		entries = append(entries, fileEntry{
			Name:            result.name,
			Status:          result.status,
			DurationSeconds: result.durationSeconds,
			OutputPath:      result.outputPath,
		})
	}

	out := filepath.Join(runDir, azldevResultsFileName)

	jsonBytes, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal results JSON:\n%w", err)
	}

	if err := os.WriteFile(out, jsonBytes, resultsFileMode); err != nil {
		return fmt.Errorf("failed to write %#q:\n%w", out, err)
	}

	return nil
}

// parseTmtResultsYAML accepts a tmt results.yaml document (a YAML sequence of result
// records) and returns the corresponding intermediate entries. Unknown fields are ignored.
func parseTmtResultsYAML(raw []byte) ([]tmtResultEntry, error) {
	// tmt's results.yaml is a sequence of mappings. Fields seen in the wild include:
	//   name, result, duration (HH:MM:SS), log (list of paths), serial-number, ...
	// We accept either a top-level sequence or a top-level mapping with a "results" key.
	type rawResult struct {
		Name     string   `yaml:"name"`
		Result   string   `yaml:"result"`
		Duration string   `yaml:"duration"`
		Log      []string `yaml:"log"`
	}

	convert := func(in []rawResult) []tmtResultEntry {
		out := make([]tmtResultEntry, 0, len(in))
		for _, raw := range in {
			entry := tmtResultEntry{
				name:            raw.Name,
				status:          raw.Result,
				durationSeconds: parseTmtDuration(raw.Duration),
			}
			if len(raw.Log) > 0 {
				entry.outputPath = raw.Log[0]
			}

			out = append(out, entry)
		}

		return out
	}

	// tmt's results.yaml is normally a top-level sequence (possibly empty when nothing
	// ran). Try sequence first; only if that parse fails, fall back to a mapping shape
	// some tmt variants/extensions use.
	var seq []rawResult
	if err := yaml.Unmarshal(raw, &seq); err == nil {
		return convert(seq), nil
	}

	// Fall back: top-level mapping with "results" key.
	var wrapper struct {
		Results []rawResult `yaml:"results"`
	}
	if err := yaml.Unmarshal(raw, &wrapper); err != nil {
		return nil, fmt.Errorf("failed to unmarshal tmt results.yaml:\n%w", err)
	}

	return convert(wrapper.Results), nil
}

// parseTmtDuration parses tmt's HH:MM:SS or MM:SS duration string into seconds.
// Returns 0 on parse failure (best-effort; the run already happened).
func parseTmtDuration(s string) float64 {
	if s == "" {
		return 0
	}

	parts := strings.Split(s, ":")

	const secsBase = 60.0

	var (
		total float64
		mult  float64 = 1
	)
	// Walk right-to-left: seconds, minutes, hours.
	for i := len(parts) - 1; i >= 0; i-- {
		var seconds float64

		_, err := fmt.Sscanf(parts[i], "%f", &seconds)
		if err != nil {
			return 0
		}

		total += seconds * mult
		mult *= secsBase
	}

	return total
}

// publishJUnit copies the JUnit XML produced by tmt to the user-requested path.
// If the source is missing (because tmt didn't get far enough to emit it), this is a
// best-effort no-op with a warning logged by the caller.
func publishJUnit(env *azldev.Env, srcPath, dstPath string) error {
	exists, err := fileutils.Exists(env.FS(), srcPath)
	if err != nil {
		return fmt.Errorf("cannot check junit src at %#q:\n%w", srcPath, err)
	}

	if !exists {
		return fmt.Errorf("junit source %#q not found (tmt may have failed before report)", srcPath)
	}

	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open junit src %#q:\n%w", srcPath, err)
	}

	defer func() { _ = src.Close() }()

	if err := fileutils.MkdirAll(env.FS(), filepath.Dir(dstPath)); err != nil {
		return fmt.Errorf("failed to ensure junit dst parent:\n%w", err)
	}

	dst, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("failed to create junit dst %#q:\n%w", dstPath, err)
	}

	defer func() { _ = dst.Close() }()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("failed to copy junit:\n%w", err)
	}

	return nil
}
