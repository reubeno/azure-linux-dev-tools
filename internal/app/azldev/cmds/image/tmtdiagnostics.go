// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package image

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/tmt"
)

const (
	// azldevResultsFileName is the file emitted by the runner with structured
	// per-test results. Format is the azldev contract for run consumers; it is the
	// human/script-friendly companion to azldev's typed-output system.
	azldevResultsFileName = "azldev-results.json"
	// azldevRunFileName is the file emitted by the runner with per-run metadata
	// (suite name, VM name, tmt version, timestamps, final status, error class).
	azldevRunFileName = "azldev-run.json"
	// hostEnvFileName captures host-side facts (OS, kernel, virsh/qemu/tmt versions,
	// etc.) at the start of each run so failure reports include them without further
	// digging.
	hostEnvFileName = "host-env.txt"

	// resultsFileMode is the permission used for files we write in the run dir.
	resultsFileMode = 0o600
	// dstDirMode is the permission used when creating parent dirs for emitted artifacts.
	dstDirMode = 0o755
)

// tmtRunMetadata is the per-run companion file written next to azldev-results.json.
// While azldev-results.json holds per-test rows, this file holds run-level metadata
// suitable for indexing/aggregation: which suite, which image, which VM, when, and
// how it ended.
type tmtRunMetadata struct {
	RunID        string `json:"runId"`
	Suite        string `json:"suite"`
	ImagePath    string `json:"imagePath"`
	Plan         string `json:"plan"`
	ProvisionHow string `json:"provisionHow"`
	VMName       string `json:"vmName,omitempty"`
	StartedAt    string `json:"startedAt"`
	FinishedAt   string `json:"finishedAt"`
	FinalStatus  string `json:"finalStatus"`
	ErrorClass   string `json:"errorClass,omitempty"`
	ErrorSummary string `json:"errorSummary,omitempty"`
}

// writeRunMetadata serializes the per-run metadata file into the run dir.
func writeRunMetadata(runDir string, meta tmtRunMetadata) error {
	out := filepath.Join(runDir, azldevRunFileName)

	jsonBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal run metadata:\n%w", err)
	}

	if err := os.WriteFile(out, jsonBytes, resultsFileMode); err != nil {
		return fmt.Errorf("failed to write %#q:\n%w", out, err)
	}

	return nil
}

// writeAzldevResultsFile emits the structured per-test results next to the run dir as
// a human/script-friendly JSON sidecar. This is the durable, on-disk form of the same
// data returned through the runner; the in-process [ImageTestResult] flow is what
// the CLI table/JSON output uses.
func writeAzldevResultsFile(runDir string, entries []tmt.ResultEntry) error {
	type fileEntry struct {
		Name            string  `json:"name"`
		Status          string  `json:"status"`
		DurationSeconds float64 `json:"durationSeconds"`
		OutputPath      string  `json:"outputPath,omitempty"`
	}

	out := make([]fileEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, fileEntry{
			Name:            e.Name,
			Status:          e.Status,
			DurationSeconds: e.Duration.Seconds(),
			OutputPath:      e.OutputPath,
		})
	}

	outPath := filepath.Join(runDir, azldevResultsFileName)

	jsonBytes, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal results JSON:\n%w", err)
	}

	if err := os.WriteFile(outPath, jsonBytes, resultsFileMode); err != nil {
		return fmt.Errorf("failed to write %#q:\n%w", outPath, err)
	}

	slog.Info("Wrote structured results", slog.String("path", outPath))

	return nil
}

// writeHostEnvFile dumps a snapshot of host-side facts (OS, kernel, virsh/qemu/tmt
// versions, KVM availability, SELinux state) into <run-dir>/host-env.txt at the start
// of each run. Failure-report quality is dramatically better when this is captured
// up-front rather than reconstructed from the user's recollection.
//
// All commands here are best-effort — if any tool is missing, we record "n/a" for
// that line rather than aborting the run.
func writeHostEnvFile(env *azldev.Env, runDir string) error {
	outPath := filepath.Join(runDir, hostEnvFileName)

	probes := []struct {
		label string
		cmd   []string
	}{
		{"timestamp", []string{"date", "-u", "+%Y-%m-%dT%H:%M:%SZ"}},
		{"uname", []string{"uname", "-a"}},
		{"os-release", []string{"cat", "/etc/os-release"}},
		{"virsh-version", []string{"virsh", "--version"}},
		{"qemu-version", []string{"qemu-system-x86_64", "-version"}},
		{"selinux", []string{"getenforce"}},
	}

	var lines strings.Builder

	lines.WriteString(fmt.Sprintf("# host environment captured for tmt run %s\n", filepath.Base(runDir)))
	lines.WriteString(fmt.Sprintf("go-arch: %s\n", runtime.GOARCH))
	lines.WriteString(fmt.Sprintf("go-os: %s\n\n", runtime.GOOS))

	for _, p := range probes {
		lines.WriteString(fmt.Sprintf("===== %s (%s) =====\n", p.label, strings.Join(p.cmd, " ")))
		lines.WriteString(captureProbeOutput(env, p.cmd))
		lines.WriteString("\n")
	}

	lines.WriteString("===== /dev/kvm =====\n")

	if _, err := os.Stat("/dev/kvm"); err == nil {
		lines.WriteString("present\n")
	} else {
		lines.WriteString("missing\n")
	}

	if err := os.WriteFile(outPath, []byte(lines.String()), resultsFileMode); err != nil {
		return fmt.Errorf("failed to write %#q:\n%w", outPath, err)
	}

	return nil
}

// captureProbeOutput runs a probe command best-effort and returns its combined
// stdout/stderr or an "n/a" marker on failure.
func captureProbeOutput(env *azldev.Env, command []string) string {
	if len(command) == 0 {
		return "(no command)\n"
	}

	probeCmd := exec.CommandContext(env, command[0], command[1:]...)

	var out strings.Builder

	probeCmd.Stdout = &out
	probeCmd.Stderr = &out

	wrapped, err := env.Command(probeCmd)
	if err != nil {
		return "n/a (failed to construct command)\n"
	}

	if err := wrapped.Run(env); err != nil {
		return fmt.Sprintf("n/a (%s)\n", err.Error())
	}

	return out.String()
}

// publishJUnit copies the JUnit XML produced by tmt to the user-requested path. If
// the source is missing (because tmt didn't get far enough to emit it), this is a
// best-effort no-op with a warning logged by the caller.
func publishJUnit(_ *azldev.Env, srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("junit source %#q not readable: %w", srcPath, err)
	}

	defer func() { _ = src.Close() }()

	if err := os.MkdirAll(filepath.Dir(dstPath), dstDirMode); err != nil {
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

// findProvisionConsoleLogs returns the absolute paths of console-log files written by
// tmt's provision step into the run dir, one per provisioned guest. tmt's `cleanup`
// step (which runs in-band thanks to tmt's --all) materializes these as real files
// inside `<run-dir>/<plan>/provision/<guest>/logs/console.txt`. Returns an empty slice
// if the provision step never produced logs.
func findProvisionConsoleLogs(runDir, plan string) []string {
	planPath := strings.TrimPrefix(plan, "/")
	provisionDir := filepath.Join(runDir, "plans", planPath, "provision")

	entries, err := os.ReadDir(provisionDir)
	if err != nil {
		return nil
	}

	var logs []string

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		candidate := filepath.Join(provisionDir, entry.Name(), "logs", "console.txt")

		if info, err := os.Stat(candidate); err == nil && info.Size() > 0 {
			logs = append(logs, candidate)
		}
	}

	return logs
}

// printFailureDiagnostics emits a "to investigate" block of concrete file pointers
// and recovery commands so the user has a single place to start diagnosing without
// grepping. The block is emitted as one slog.Error line followed by per-pointer info
// lines so it is visible without --verbose.
func printFailureDiagnostics(runDir, suiteName, plan string, result *tmt.Result) {
	errorClass := ""
	errorSummary := ""
	vmName := ""

	if result != nil {
		errorClass = result.ErrorClass
		errorSummary = result.ErrorSummary
		vmName = result.VMName
	}

	if errorClass == "" {
		errorClass = "unknown"
	}

	if errorSummary == "" {
		slog.Error("tmt suite failed",
			slog.String("suite", suiteName),
			slog.String("errorClass", errorClass),
			slog.String("run-dir", runDir))
	} else {
		slog.Error("tmt suite failed",
			slog.String("suite", suiteName),
			slog.String("errorClass", errorClass),
			slog.String("summary", errorSummary),
			slog.String("run-dir", runDir))
	}

	type pointer struct {
		label string
		path  string
	}

	var pointers []pointer

	add := func(label, path string) {
		if path == "" {
			return
		}

		if _, err := os.Stat(path); err == nil {
			pointers = append(pointers, pointer{label: label, path: path})
		}
	}

	add("tmt run log", filepath.Join(runDir, "log.txt"))
	add("tmt stdout/stderr", filepath.Join(runDir, tmt.OutputLogFileName))

	for _, consoleLog := range findProvisionConsoleLogs(runDir, plan) {
		add("serial console", consoleLog)
	}

	add("structured results JSON", filepath.Join(runDir, azldevResultsFileName))
	add("per-run metadata", filepath.Join(runDir, azldevRunFileName))
	add("host environment", filepath.Join(runDir, hostEnvFileName))

	if vmName != "" {
		// Session libvirt qemu log lives under the user's XDG_CACHE_HOME (typically
		// ~/.cache/libvirt). System libvirt would be /var/log/libvirt/qemu/<n>.log.
		if home, err := os.UserHomeDir(); err == nil {
			add("libvirt session qemu log",
				filepath.Join(home, ".cache", "libvirt", "qemu", "log", vmName+".log"))
		}
		// testcloud's per-VM dir lives as a sibling of the run dir under the
		// workdir-root we pass to tmt: <run-dir-parent>/testcloud/instances/<vm-name>/.
		add("testcloud instance dir",
			filepath.Join(filepath.Dir(runDir), "testcloud", "instances", vmName))
	}

	// Per-step results.yaml entries (only when non-empty and present).
	for _, step := range []string{"discover", "prepare", "execute"} {
		path := filepath.Join(runDir, "plans",
			strings.TrimPrefix(plan, "/"), step, "results.yaml")
		if info, err := os.Stat(path); err == nil && info.Size() > int64(len("[]\n")) {
			pointers = append(pointers, pointer{
				label: step + " step results.yaml",
				path:  path,
			})
		}
	}

	if len(pointers) == 0 {
		return
	}

	slog.Error("To investigate, see the following files:")

	for _, p := range pointers {
		slog.Error("  - "+p.label, slog.String("path", p.path))
	}

	if vmName != "" {
		slog.Error("Leaked VM name (cleanup runs automatically; for manual: " +
			"<venv>/bin/tmt clean guests -i " + runDir + "): " + vmName)
	}
}
