// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tmt

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"gopkg.in/yaml.v3"
)

func collectRunResults(fs opctx.FS, junitPath string, result *testframework.RunResult) error {
	entries, err := CollectResults(fs, result.OutputDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		result.Cases = append(result.Cases, testframework.CaseResult{
			Name: entry.Name, Status: entry.Status, Duration: entry.Duration, OutputPath: entry.OutputPath,
		})
	}

	if junitPath == "" {
		return nil
	}

	if len(entries) == 0 {
		if result.Status == testframework.Passed || result.Status == testframework.Skipped {
			return errors.New("TMT completed without native test results for the requested JUnit report")
		}

		return nil
	}

	if err := WriteJUnit(fs, junitPath, entries); err != nil {
		return err
	}

	result.JUnitPath = junitPath

	return nil
}

const (
	yamlStringTag          = "!!str"
	junitFailure           = "failure"
	junitError             = "error"
	junitSkipped           = "skipped"
	yamlPairWidth          = 2
	hourMinuteSecondFields = 3
)

// ResultEntry preserves one native TMT test outcome, not a synthetic run-level test.
type ResultEntry struct {
	Name       string        `json:"name"`
	Status     string        `json:"status"`
	Duration   time.Duration `json:"duration"`
	OutputPath string        `json:"outputPath,omitempty"`
}

// CollectResults reads only plan-level 'execute/results.yaml' files. Missing run
// directories or result files are valid when provisioning failed before execution.
// Returned output paths are absolute, contained in nativeRunDir and free of symlinks.
func CollectResults(fs opctx.FS, nativeRunDir string) ([]ResultEntry, error) {
	if !filepath.IsAbs(nativeRunDir) {
		return nil, errors.New("native TMT run directory must be absolute")
	}

	nativeRunDir = filepath.Clean(nativeRunDir)

	if err := rejectResultSymlinks(fs, nativeRunDir); err != nil {
		return nil, err
	}

	exists, err := fileutils.Exists(fs, nativeRunDir)
	if err != nil {
		return nil, fmt.Errorf("check native TMT run directory %#q:\n%w", nativeRunDir, err)
	}

	if !exists {
		return nil, nil
	}

	entries, err := collectPlanResults(fs, nativeRunDir, nativeRunDir)
	if err != nil {
		return nil, fmt.Errorf("collect TMT results in %#q:\n%w", nativeRunDir, err)
	}

	return entries, nil
}

func collectPlanResults(fs opctx.FS, root, directory string) ([]ResultEntry, error) {
	children, err := fileutils.ReadDir(fs, directory)
	if err != nil {
		return nil, fmt.Errorf("read TMT result directory %#q:\n%w", directory, err)
	}

	slices.SortFunc(children, func(left, right os.FileInfo) int {
		return strings.Compare(left.Name(), right.Name())
	})

	var entries []ResultEntry

	for _, child := range children {
		if !child.IsDir() || child.Mode()&os.ModeSymlink != 0 {
			continue
		}

		path := filepath.Join(directory, child.Name())
		if err := rejectResultSymlinks(fs, path); err != nil {
			return nil, err
		}

		var results []ResultEntry

		switch child.Name() {
		case "execute":
			results, err = readPlanResults(fs, root, path)
		case "discover", "provision", "prepare", "report", "finish", "cleanup":
			// These are step workspaces, not nested plan directories.
			continue
		default:
			results, err = collectPlanResults(fs, root, path)
		}

		if err != nil {
			return nil, err
		}

		entries = append(entries, results...)
	}

	return entries, nil
}

func readPlanResults(fs opctx.FS, root, executeDir string) ([]ResultEntry, error) {
	path := filepath.Join(executeDir, "results.yaml")
	if err := rejectResultSymlinks(fs, path); err != nil {
		return nil, err
	}

	raw, err := fileutils.ReadFile(fs, path)
	if errors.Is(err, iofs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read %#q:\n%w", path, err)
	}

	nodes, err := resultNodes(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %#q:\n%w", path, err)
	}

	entries := make([]ResultEntry, 0, len(nodes))

	for index, node := range nodes {
		entry, err := decodeResult(fs, root, executeDir, node)
		if err != nil {
			return nil, fmt.Errorf("parse %#q, result %d:\n%w", path, index+1, err)
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

func resultNodes(raw []byte) ([]*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))

	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode results document:\n%w", err)
	}

	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("decode trailing results document:\n%w", err)
		}

		return nil, errors.New("expected exactly one results document")
	}

	if len(document.Content) != 1 {
		return nil, errors.New("expected a results sequence")
	}

	node := document.Content[0]
	if node.Kind == yaml.MappingNode {
		fields, err := resultFields(node)
		if err != nil {
			return nil, err
		}

		node = fields["results"]
	}

	if node == nil || node.Kind != yaml.SequenceNode {
		return nil, errors.New("expected a results sequence or a mapping containing a 'results' sequence")
	}

	return node.Content, nil
}

func resultFields(node *yaml.Node) (map[string]*yaml.Node, error) {
	if node.Kind != yaml.MappingNode {
		return nil, errors.New("result must be a mapping")
	}

	fields := make(map[string]*yaml.Node, len(node.Content)/yamlPairWidth)

	for index := 0; index < len(node.Content); index += 2 {
		key := node.Content[index]
		if key.Kind != yaml.ScalarNode || key.Tag != yamlStringTag {
			return nil, errors.New("result field names must be strings")
		}

		if _, duplicate := fields[key.Value]; duplicate {
			return nil, fmt.Errorf("duplicate result field %#q", key.Value)
		}

		fields[key.Value] = node.Content[index+1]
	}

	return fields, nil
}

func resultString(fields map[string]*yaml.Node, name string, optional bool) (string, error) {
	node := fields[name]
	if node == nil || node.Tag == "!!null" {
		if optional {
			return "", nil
		}

		return "", fmt.Errorf("missing result field %#q", name)
	}

	if node.Kind != yaml.ScalarNode || node.Tag != yamlStringTag {
		return "", fmt.Errorf("result field %#q must be a string", name)
	}

	if !optional && strings.TrimSpace(node.Value) == "" {
		return "", fmt.Errorf("result field %#q must not be empty", name)
	}

	return node.Value, nil
}

func decodeResult(fs opctx.FS, root, executeDir string, node *yaml.Node) (ResultEntry, error) {
	var entry ResultEntry

	fields, err := resultFields(node)
	if err != nil {
		return entry, err
	}

	entry.Name, err = resultString(fields, "name", false)
	if err != nil {
		return entry, err
	}

	entry.Status, err = resultString(fields, "result", false)
	if err != nil {
		return entry, err
	}

	if _, err := resultCategory(entry.Status); err != nil {
		return entry, err
	}

	duration, err := resultString(fields, "duration", true)
	if err != nil {
		return entry, err
	}

	entry.Duration, err = parseResultDuration(duration)
	if err != nil {
		return entry, fmt.Errorf("result duration %#q:\n%w", duration, err)
	}

	entry.OutputPath, err = resultOutputPath(fs, root, executeDir, fields["log"])

	return entry, err
}

func parseResultDuration(value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}

	parts := strings.Split(value, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, errors.New("expected 'HH:MM:SS' or 'MM:SS'")
	}

	for _, part := range parts[:len(parts)-1] {
		if part == "" || strings.ContainsFunc(part, func(char rune) bool { return char < '0' || char > '9' }) {
			return 0, errors.New("hours and minutes must be nonnegative integers")
		}
	}

	seconds := parts[len(parts)-1]
	if err := validateClockSeconds(seconds); err != nil {
		return 0, err
	}

	duration := parts[0] + "m" + seconds + "s"
	if len(parts) == hourMinuteSecondFields {
		minutes, err := strconv.ParseUint(parts[1], 10, 8)
		if err != nil || minutes >= 60 {
			return 0, errors.New("minutes must be between '00' and '59'")
		}

		duration = parts[0] + "h" + parts[1] + "m" + seconds + "s"
	}

	parsed, err := time.ParseDuration(duration)
	if err != nil {
		return 0, fmt.Errorf("invalid or overflowing duration:\n%w", err)
	}

	return parsed, nil
}

func validateClockSeconds(value string) error {
	whole, fraction, fractional := strings.Cut(value, ".")

	seconds, err := strconv.ParseUint(whole, 10, 8)
	if err != nil || seconds >= 60 || len(whole) != 2 {
		return errors.New("seconds must be between '00' and '59'")
	}

	const nanosecondPrecision = 9

	if fractional && (fraction == "" || len(fraction) > nanosecondPrecision ||
		strings.ContainsFunc(fraction, func(char rune) bool { return char < '0' || char > '9' })) {
		return errors.New("fractional seconds must contain one to nine digits")
	}

	return nil
}

func resultOutputPath(fs opctx.FS, root, executeDir string, logs *yaml.Node) (string, error) {
	if logs == nil {
		return "", nil
	}

	if logs.Kind != yaml.SequenceNode {
		return "", errors.New("result 'log' must be an array of strings")
	}

	var first string

	for _, log := range logs.Content {
		if log.Kind != yaml.ScalarNode || log.Tag != yamlStringTag || log.Value == "" {
			return "", errors.New("result 'log' entries must be nonempty strings")
		}

		path, err := resolveResultPath(fs, root, executeDir, log.Value)
		if err != nil {
			return "", err
		}

		if first == "" {
			first = path
		}
	}

	return first, nil
}

func resolveResultPath(fs opctx.FS, root, executeDir, log string) (string, error) {
	if strings.ContainsAny(log, "\\\x00") {
		return "", fmt.Errorf("unsafe result output path %#q", log)
	}

	path := log
	if !filepath.IsAbs(path) {
		path = filepath.Join(executeDir, path)
	}

	path = filepath.Clean(path)

	relative, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(relative) || relative == "." {
		return "", fmt.Errorf("result output path %#q escapes native run directory", log)
	}

	if err := rejectResultSymlinks(fs, path); err != nil {
		return "", err
	}

	info, err := fs.Stat(path)
	if err != nil && !errors.Is(err, iofs.ErrNotExist) {
		return "", fmt.Errorf("check result output %#q:\n%w", path, err)
	}

	if err == nil && !info.Mode().IsRegular() {
		return "", fmt.Errorf("result output %#q is not a regular file", path)
	}

	return path, nil
}

func rejectResultSymlinks(fs opctx.FS, path string) error {
	// Filesystems without this optional afero capability (e.g. memfs) cannot
	// create symlinks. Reject rather than follow links on real filesystems.
	lstat, supported := fs.(interface {
		LstatIfPossible(path string) (os.FileInfo, bool, error)
	})
	if !supported {
		return nil
	}

	for current := path; ; current = filepath.Dir(current) {
		info, _, err := lstat.LstatIfPossible(current)
		if err != nil && !errors.Is(err, iofs.ErrNotExist) {
			return fmt.Errorf("inspect result path %#q:\n%w", current, err)
		}

		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("result path %#q contains a symlink at %#q", path, current)
		}

		if current == filepath.Dir(current) {
			break
		}
	}

	return nil
}

// resultCategory matches the pinned native JUnit reporter, including 'pending'.
// In particular, 'info' is not a pass, and 'warn' is an error rather than a pass.
func resultCategory(status string) (string, error) {
	switch status {
	case "pass":
		return "pass", nil
	case "fail":
		return junitFailure, nil
	case junitError, "warn", "pending":
		return junitError, nil
	case "skip", "info":
		return junitSkipped, nil
	default:
		return "", fmt.Errorf("unknown TMT result status %#q", status)
	}
}

type junitIssue struct {
	Type    string `xml:"type,attr"`
	Message string `xml:"message,attr"`
	Detail  string `xml:",chardata"`
}

type junitCase struct {
	Name    string      `xml:"name,attr"`
	Status  string      `xml:"status,attr"`
	Time    string      `xml:"time,attr"`
	Failure *junitIssue `xml:"failure,omitempty"`
	Error   *junitIssue `xml:"error,omitempty"`
	Skipped *junitIssue `xml:"skipped,omitempty"`
}

type junitSuite struct {
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Errors   int         `xml:"errors,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Time     string      `xml:"time,attr"`
	Cases    []junitCase `xml:"testcase"`
}

// WriteJUnit writes actual per-case results without replacing native plan reporters.
// Failure logs are bounded to 4 KiB; missing logs retain their path in the report.
func WriteJUnit(fs opctx.FS, destination string, entries []ResultEntry) error {
	suite := junitSuite{Name: "tmt", Tests: len(entries)}

	var seconds float64

	for _, entry := range entries {
		testcase, category, err := makeJUnitCase(fs, entry)
		if err != nil {
			return fmt.Errorf("make JUnit case %#q:\n%w", entry.Name, err)
		}

		switch category {
		case junitFailure:
			suite.Failures++
		case junitError:
			suite.Errors++
		case junitSkipped:
			suite.Skipped++
		}

		seconds += entry.Duration.Seconds()

		suite.Cases = append(suite.Cases, testcase)
	}

	suite.Time = strconv.FormatFloat(seconds, 'f', -1, 64)

	report := struct {
		XMLName xml.Name   `xml:"testsuites"`
		Suite   junitSuite `xml:"testsuite"`
	}{Suite: suite}

	data, err := xml.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode TMT JUnit report:\n%w", err)
	}

	if err := fileutils.MkdirAll(fs, filepath.Dir(destination)); err != nil {
		return fmt.Errorf("create JUnit destination directory:\n%w", err)
	}

	reportBytes := []byte(xml.Header + string(data) + "\n")
	if err := fileutils.WriteFile(fs, destination, reportBytes, fileperms.PrivateFile); err != nil {
		return fmt.Errorf("write TMT JUnit report %#q:\n%w", destination, err)
	}

	return nil
}

func makeJUnitCase(fs opctx.FS, entry ResultEntry) (testcase junitCase, category string, err error) {
	if strings.TrimSpace(entry.Name) == "" || entry.Duration < 0 {
		return testcase, "", errors.New("test name must be nonempty and duration must not be negative")
	}

	category, err = resultCategory(entry.Status)
	if err != nil {
		return testcase, "", err
	}

	testcase = junitCase{
		Name: entry.Name, Status: entry.Status, Time: strconv.FormatFloat(entry.Duration.Seconds(), 'f', -1, 64),
	}

	issue := &junitIssue{Type: entry.Status, Message: "TMT result: " + entry.Status}

	switch category {
	case junitFailure, junitError:
		issue.Detail = resultFailureOutput(fs, entry.OutputPath)
		if category == junitFailure {
			testcase.Failure = issue
		} else {
			testcase.Error = issue
		}
	case junitSkipped:
		testcase.Skipped = issue
	}

	return testcase, category, nil
}

func resultFailureOutput(fs opctx.FS, path string) string {
	if path == "" {
		return ""
	}

	detail := "Output: " + path

	if err := rejectResultSymlinks(fs, path); err != nil {
		return detail + "\nOutput unavailable: " + err.Error()
	}

	info, err := fs.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return detail + "\nOutput unavailable."
	}

	file, err := fs.Open(path)
	if err != nil {
		return detail + "\nOutput unavailable: " + err.Error()
	}

	const excerptLimit = 4096

	data, readErr := io.ReadAll(io.LimitReader(file, excerptLimit+1))

	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return detail + "\nOutput unavailable: " + err.Error()
	}

	if len(data) > excerptLimit {
		return detail + "\n" + string(data[:excerptLimit]) + "\n[output truncated]"
	}

	return detail + "\n" + string(data)
}
