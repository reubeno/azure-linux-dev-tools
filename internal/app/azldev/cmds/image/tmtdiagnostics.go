// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package image

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
)

// tmtRunMetadata is the per-run companion file written next to azldev-results.json.
// While azldev-results.json holds per-test rows, this file holds run-level metadata
// suitable for indexing/aggregation: which suite, which image, which VM, when, and how
// it ended.
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

// writeHostEnvFile dumps a snapshot of host-side facts (OS, kernel, virsh/qemu/tmt
// versions, KVM availability, SELinux state) into <run-dir>/host-env.txt at the start
// of each run. Failure-report quality is dramatically better when this is captured
// up-front rather than reconstructed from the user's recollection.
//
// All commands here are best-effort — if any tool is missing, we record "n/a" for that
// line rather than aborting the run.
func writeHostEnvFile(env *azldev.Env, prep *tmtRunPrep) error {
	out := filepath.Join(prep.runDir, hostEnvFileName)

	probes := []struct {
		label string
		cmd   []string
	}{
		{"timestamp", []string{"date", "-u", "+%Y-%m-%dT%H:%M:%SZ"}},
		{"uname", []string{"uname", "-a"}},
		{"os-release", []string{"cat", "/etc/os-release"}},
		{"virsh-version", []string{"virsh", "--version"}},
		{"qemu-version", []string{"qemu-system-x86_64", "-version"}},
		{"tmt-version", []string{filepath.Join(prep.venvDir, "bin", tmtBinName), "--version"}},
		{"selinux", []string{"getenforce"}},
	}

	var lines strings.Builder

	lines.WriteString(fmt.Sprintf("# host environment captured for tmt run %s\n", filepath.Base(prep.runDir)))
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

	if err := os.WriteFile(out, []byte(lines.String()), resultsFileMode); err != nil {
		return fmt.Errorf("failed to write %#q:\n%w", out, err)
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

// findProvisionConsoleLogs returns the absolute paths of console-log files written by
// tmt's provision step into the run dir, one per provisioned guest. tmt's `cleanup`
// step (which we always enable via `--all`) materializes these as real files inside
// `<run-dir>/plans/<plan>/provision/<guest>/logs/console.txt`. Returns an empty slice
// if the provision step never produced logs.
func findProvisionConsoleLogs(prep *tmtRunPrep) []string {
	planPath := strings.TrimPrefix(prep.tmtConfig.Plan, "/")
	provisionDir := filepath.Join(prep.runDir, planPath, "provision")

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

// extractAndPersistResults parses tmt's results.yaml and writes the structured JSON
// sidecar. Best-effort: returns whatever entries it could extract; warnings are logged
// but never fatal.
func extractAndPersistResults(env *azldev.Env, prep *tmtRunPrep, suiteName string) []tmtResultEntry {
	tmtResults, extractErr := extractTmtResults(env, prep.runDir, prep.tmtConfig.Plan)
	if extractErr != nil {
		slog.Warn("Failed to extract structured tmt results",
			slog.String("suite", suiteName),
			slog.String("err", extractErr.Error()))

		return tmtResults
	}

	if writeErr := writeAzldevResultsFile(prep.runDir, tmtResults); writeErr != nil {
		slog.Warn("Failed to write structured results JSON",
			slog.String("suite", suiteName),
			slog.String("err", writeErr.Error()))
	} else {
		slog.Info("Wrote structured results",
			slog.String("suite", suiteName),
			slog.String("path", filepath.Join(prep.runDir, azldevResultsFileName)))
	}

	return tmtResults
}

// publishJUnitIfRequested copies the in-run-dir junit.xml that tmt's JUnit reporter
// produced (if any) to the user-supplied --junit-xml path. No-op when --junit-xml
// wasn't requested.
func publishJUnitIfRequested(
	env *azldev.Env, prep *tmtRunPrep, options *ImageTestOptions, suiteName string,
) {
	if prep.junitOutPath == "" {
		return
	}

	if junitErr := publishJUnit(env, prep.junitOutPath, options.JUnitXMLPath); junitErr != nil {
		slog.Warn("Failed to publish JUnit XML",
			slog.String("suite", suiteName),
			slog.String("from", prep.junitOutPath),
			slog.String("to", options.JUnitXMLPath),
			slog.String("err", junitErr.Error()))
	}
}

// finalizeRun captures the VM name (when discoverable), writes the per-run metadata
// sidecar, and prints either a "to investigate" diagnostic block (on failure) or a
// success log line.
func finalizeRun(
	env *azldev.Env, prep *tmtRunPrep, options *ImageTestOptions,
	suiteName string, runErr error, errorClass, errorSummary string,
	startedAt time.Time,
) {
	// Capture the libvirt domain name for the run, if discoverable. This makes manual
	// cleanup unambiguous when our defer is bypassed.
	vmName := captureVMName(env, prep)

	finalStatus := TestStatusPass
	if runErr != nil {
		finalStatus = TestStatusError
	}

	meta := tmtRunMetadata{
		RunID:        filepath.Base(prep.runDir),
		Suite:        suiteName,
		ImagePath:    options.ImagePath,
		Plan:         prep.tmtConfig.Plan,
		ProvisionHow: string(prep.tmtConfig.Provision.How),
		VMName:       vmName,
		StartedAt:    startedAt.Format(time.RFC3339),
		FinishedAt:   time.Now().UTC().Format(time.RFC3339),
		FinalStatus:  finalStatus,
		ErrorClass:   errorClass,
		ErrorSummary: errorSummary,
	}

	if metaErr := writeRunMetadata(prep.runDir, meta); metaErr != nil {
		slog.Warn("Failed to write per-run metadata",
			slog.String("suite", suiteName),
			slog.String("err", metaErr.Error()))
	}

	if runErr != nil {
		printFailureDiagnostics(prep, vmName, errorClass, errorSummary)
	} else {
		slog.Info("tmt suite passed",
			slog.String("suite", suiteName),
			slog.String("run-dir", prep.runDir))
	}
}

// captureVMName returns the libvirt domain name that tmt assigned to this run, or "" if
// it can't be determined. We parse <run-dir>/log.txt for the `name: tmt-XXX-YYYY` line
// that tmt's testcloud plugin emits during provision. This is the authoritative source
// since the name is deterministic only as a *prefix* (the random suffix is only known
// to the live tmt process), and the log records the exact value.
func captureVMName(_ *azldev.Env, prep *tmtRunPrep) string {
	return vmNameFromTmtLog(prep.runDir)
}

// vmNameRe matches the testcloud-managed libvirt domain name in tmt's run log:
// `name: tmt-XXX-YYYYYYYY`.
var vmNameRe = regexp.MustCompile(`(?m)^\s*name:\s*(tmt-[A-Za-z0-9-]+)\s*$`)

func vmNameFromTmtLog(runDir string) string {
	logPath := filepath.Join(runDir, "log.txt")

	raw, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}

	matches := vmNameRe.FindAllStringSubmatch(string(raw), -1)
	if len(matches) == 0 {
		return ""
	}

	return matches[len(matches)-1][1]
}

// classifyTmtFailure returns a short stable error class and a one-sentence proximate
// cause for a failed tmt run. The classification is heuristic — we read <run-dir>/log.txt
// for known patterns. If the log can't be parsed, we fall back to a generic class with
// the original error's first line as the summary.
func classifyTmtFailure(runDir string, runErr error) (errorClass, summary string) {
	logPath := filepath.Join(runDir, "log.txt")

	raw, readErr := os.ReadFile(logPath)
	if readErr != nil {
		return "tmt-internal", firstLine(runErr.Error())
	}

	logText := string(raw)

	patterns := []struct {
		class string
		regex *regexp.Regexp
	}{
		{"boot-timeout", regexp.MustCompile(`(?i)Failed to boot.*has failed to boot in (\d+) seconds`)},
		{"boot-timeout", regexp.MustCompile(`(?i)Failed to boot testcloud instance`)},
		{"ssh-timeout", regexp.MustCompile(`(?i)guest is not reachable|connection.*refused|ssh.*timed? out|cannot connect`)},
		{"provision-failed", regexp.MustCompile(`(?i)provision step failed`)},
		{"execute-failed", regexp.MustCompile(`(?i)execute step failed|test.*failed`)},
	}

	for _, p := range patterns {
		if loc := p.regex.FindStringIndex(logText); loc != nil {
			return p.class, extractMatchedLine(logText, loc[0])
		}
	}

	return "unknown", firstLine(runErr.Error())
}

// extractMatchedLine returns the line containing the byte offset `offset` in text, trimmed.
func extractMatchedLine(text string, offset int) string {
	if offset < 0 || offset >= len(text) {
		return ""
	}

	start := offset
	for start > 0 && text[start-1] != '\n' {
		start--
	}

	end := offset
	for end < len(text) && text[end] != '\n' {
		end++
	}

	return strings.TrimSpace(text[start:end])
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}

	return ""
}

// runExtraArgsHaveVerbosity returns whether the user already supplied a -v / -vv / -vvv
// (or --verbose) flag in run-extra-args. We only auto-inject -vv when they haven't.
func runExtraArgsHaveVerbosity(args []string) bool {
	for _, arg := range args {
		switch {
		case arg == "-v" || arg == "-vv" || arg == "-vvv" || arg == "-vvvv":
			return true
		case arg == "--verbose":
			return true
		case strings.HasPrefix(arg, "--verbose="):
			return true
		}
	}

	return false
}

// printFailureDiagnostics emits a "to investigate" block of concrete file pointers and
// recovery commands so the user has a single place to start diagnosing without grepping.
// The block is emitted as one slog.Error line followed by per-pointer info lines so it
// is visible without --verbose.
func printFailureDiagnostics(prep *tmtRunPrep, vmName, errorClass, errorSummary string) {
	if errorClass == "" {
		errorClass = "unknown"
	}

	// One-line summary: the tweet-sized triage line.
	if errorSummary == "" {
		slog.Error("tmt suite failed",
			slog.String("suite", prep.suiteName),
			slog.String("errorClass", errorClass),
			slog.String("run-dir", prep.runDir))
	} else {
		slog.Error("tmt suite failed",
			slog.String("suite", prep.suiteName),
			slog.String("errorClass", errorClass),
			slog.String("summary", errorSummary),
			slog.String("run-dir", prep.runDir))
	}

	// Build concrete pointers, only including ones that exist on disk so users don't
	// chase phantoms.
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

	add("tmt run log", filepath.Join(prep.runDir, "log.txt"))
	add("tmt stdout/stderr", filepath.Join(prep.runDir, tmtOutputFileName))

	for _, consoleLog := range findProvisionConsoleLogs(prep) {
		add("serial console", consoleLog)
	}

	add("structured results JSON", filepath.Join(prep.runDir, azldevResultsFileName))
	add("per-run metadata", filepath.Join(prep.runDir, azldevRunFileName))
	add("host environment", filepath.Join(prep.runDir, hostEnvFileName))

	if vmName != "" {
		// Session libvirt qemu log lives under the user's XDG_CACHE_HOME (typically
		// ~/.cache/libvirt). System libvirt would be /var/log/libvirt/qemu/<name>.log.
		if home, err := os.UserHomeDir(); err == nil {
			add("libvirt session qemu log",
				filepath.Join(home, ".cache", "libvirt", "qemu", "log", vmName+".log"))
		}
		// testcloud's per-VM dir lives next to the run dir under the workdir-root we
		// pass to tmt: <run-dir-parent>/testcloud/instances/<vm-name>/.
		add("testcloud instance dir",
			filepath.Join(filepath.Dir(prep.runDir), "testcloud", "instances", vmName))
	}

	// Step results.yaml entries (only when non-empty and present).
	for _, step := range []string{"discover", "prepare", "execute"} {
		path := filepath.Join(prep.runDir, "plans",
			strings.TrimPrefix(prep.tmtConfig.Plan, "/"), step, "results.yaml")
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
			"<venv>/bin/testcloud remove -f " + vmName + "):")
	}
}
