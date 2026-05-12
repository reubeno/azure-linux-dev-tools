// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tmt

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"gopkg.in/yaml.v3"
)

// parseRunResults reads tmt's results.yaml for the given plan under runDir and returns
// the parsed entries. Missing files surface as an empty slice with no error — the
// caller distinguishes "no test ever ran" from "test ran but failed" by inspecting the
// number of entries returned (and the runErr from [Runner.Run]).
func parseRunResults(fs opctx.FS, runDir, plan string) ([]ResultEntry, error) {
	// tmt writes results to <run-dir>/<plan-relative-path>/execute/results.yaml.
	// Plan names start with '/'; the on-disk path strips the leading slash.
	planPath := strings.TrimPrefix(plan, "/")
	resultsPath := filepath.Join(runDir, planPath, "execute", "results.yaml")

	exists, err := fileutils.Exists(fs, resultsPath)
	if err != nil {
		return nil, fmt.Errorf("cannot check results at %#q:\n%w", resultsPath, err)
	}

	if !exists {
		// tmt didn't get far enough to produce results — that's not an error per se,
		// it just means we have nothing to report.
		return nil, nil
	}

	raw, err := fileutils.ReadFile(fs, resultsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read %#q:\n%w", resultsPath, err)
	}

	results, err := parseResultsYAML(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse results.yaml:\n%w", err)
	}

	return results, nil
}

// parseResultsYAML accepts a tmt results.yaml document (a YAML sequence of result
// records) and returns the corresponding [ResultEntry] slice. Unknown fields are
// ignored. Accepts either a top-level sequence (the common case) or a top-level
// mapping with a "results" key (some tmt variants).
func parseResultsYAML(raw []byte) ([]ResultEntry, error) {
	// tmt's results.yaml is a sequence of mappings. Fields seen in the wild include:
	//   name, result, duration (HH:MM:SS), log (list of paths), serial-number, ...
	type rawResult struct {
		Name     string   `yaml:"name"`
		Result   string   `yaml:"result"`
		Duration string   `yaml:"duration"`
		Log      []string `yaml:"log"`
	}

	convert := func(in []rawResult) []ResultEntry {
		out := make([]ResultEntry, 0, len(in))
		for _, raw := range in {
			entry := ResultEntry{
				Name:     raw.Name,
				Status:   raw.Result,
				Duration: parseTmtDuration(raw.Duration),
			}
			if len(raw.Log) > 0 {
				entry.OutputPath = raw.Log[0]
			}

			out = append(out, entry)
		}

		return out
	}

	// Try sequence first (the common case).
	var seq []rawResult
	if err := yaml.Unmarshal(raw, &seq); err == nil {
		return convert(seq), nil
	}

	// Fall back to a top-level mapping with a "results" key.
	var wrapper struct {
		Results []rawResult `yaml:"results"`
	}
	if err := yaml.Unmarshal(raw, &wrapper); err != nil {
		return nil, fmt.Errorf("failed to unmarshal tmt results.yaml:\n%w", err)
	}

	return convert(wrapper.Results), nil
}

// parseTmtDuration parses tmt's HH:MM:SS or MM:SS duration string into a [time.Duration].
// Returns zero on parse failure (best-effort: the run already happened).
func parseTmtDuration(s string) time.Duration {
	if s == "" {
		return 0
	}

	parts := strings.Split(s, ":")

	const secsBase = 60.0

	var (
		totalSecs float64
		mult      float64 = 1
	)
	// Walk right-to-left: seconds, minutes, hours.
	for i := len(parts) - 1; i >= 0; i-- {
		var seconds float64

		_, err := fmt.Sscanf(parts[i], "%f", &seconds)
		if err != nil {
			return 0
		}

		totalSecs += seconds * mult
		mult *= secsBase
	}

	return time.Duration(totalSecs * float64(time.Second))
}
