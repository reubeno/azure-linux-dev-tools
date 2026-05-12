// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tmt

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Error class values returned by [classifyFailure]. These are stable identifiers
// callers can match on; the human-readable proximate cause is a separate string.
const (
	ErrorClassBootTimeout   = "boot-timeout"
	ErrorClassSSHTimeout    = "ssh-timeout"
	ErrorClassProvisionFail = "provision-failed"
	ErrorClassExecuteFail   = "execute-failed"
	ErrorClassTmtInternal   = "tmt-internal"
	ErrorClassUnknown       = "unknown"
)

// vmNameRe matches the testcloud-managed libvirt domain name in tmt's run log:
// `name: tmt-XXX-YYYYYYYY`.
//

var vmNameRe = regexp.MustCompile(`(?m)^\s*name:\s*(tmt-[A-Za-z0-9-]+)\s*$`)

// vmNameFromTmtLog returns the libvirt domain name that tmt assigned to this run, or
// "" if it can't be determined. tmt's testcloud plugin emits `name: tmt-XXX-YYYY` to
// the run log during provision; we parse the last occurrence (which corresponds to
// the most recently provisioned guest, in case there were retries).
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

// failurePatterns is the heuristic set of regexes [classifyFailure] tries against tmt's
// run log to extract a stable error class. First-match wins; ordering is from most
// specific to least.
//
//nolint:gochecknoglobals // const-equivalent compiled regexes.
var failurePatterns = []struct {
	class string
	regex *regexp.Regexp
}{
	{ErrorClassBootTimeout, regexp.MustCompile(`(?i)Failed to boot.*has failed to boot in (\d+) seconds`)},
	{ErrorClassBootTimeout, regexp.MustCompile(`(?i)Failed to boot testcloud instance`)},
	{ErrorClassSSHTimeout, regexp.MustCompile(
		`(?i)guest is not reachable|connection.*refused|ssh.*timed? out|cannot connect`)},
	{ErrorClassProvisionFail, regexp.MustCompile(`(?i)provision step failed`)},
	{ErrorClassExecuteFail, regexp.MustCompile(`(?i)execute step failed|test.*failed`)},
}

// classifyFailure returns a short stable error class and a one-sentence proximate
// cause for a failed tmt run. The classification is heuristic — we read
// `<run-dir>/log.txt` for known patterns. If the log can't be parsed, we fall back to
// a generic class with the original error's first line as the summary.
func classifyFailure(runDir string, runErr error) (class, summary string) {
	logPath := filepath.Join(runDir, "log.txt")

	raw, err := os.ReadFile(logPath)
	if err != nil {
		return ErrorClassTmtInternal, firstLine(runErr.Error())
	}

	logText := string(raw)

	for _, p := range failurePatterns {
		if loc := p.regex.FindStringIndex(logText); loc != nil {
			return p.class, extractMatchedLine(logText, loc[0])
		}
	}

	return ErrorClassUnknown, firstLine(runErr.Error())
}

// extractMatchedLine returns the line containing the byte offset `offset` in text,
// trimmed.
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
