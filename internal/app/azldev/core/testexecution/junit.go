// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testexecution

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
)

type junitSuites struct {
	XMLName xml.Name     `xml:"testsuites"`
	Suites  []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name     string `xml:"name,attr"`
	Tests    string `xml:"tests,attr,omitempty"`
	Failures string `xml:"failures,attr,omitempty"`
	Errors   string `xml:"errors,attr,omitempty"`
	Skipped  string `xml:"skipped,attr,omitempty"`
	Body     string `xml:",innerxml"`
}

type junitCase struct {
	XMLName xml.Name      `xml:"testcase"`
	Name    string        `xml:"name,attr"`
	Time    string        `xml:"time,attr"`
	Failure *junitMessage `xml:"failure,omitempty"`
	Error   *junitMessage `xml:"error,omitempty"`
	Skipped *junitMessage `xml:"skipped,omitempty"`
}

type junitMessage struct {
	Message string `xml:"message,attr"`
}

// writeJUnit preserves native test cases when available and emits explicit orchestration outcomes otherwise.
func writeJUnit(files opctx.FS, path string, results []testframework.RunResult) error {
	report := junitSuites{}

	for _, result := range results {
		native, err := readJUnit(files, result.JUnitPath)
		if err != nil {
			return fmt.Errorf("write j unit:\n%w", err)
		}

		report.Suites = append(report.Suites, native...)

		needsOutcome, err := needsRunOutcome(result.Status, native)
		if err != nil {
			return err
		}

		if !needsOutcome {
			continue
		}

		test := junitCase{Name: result.Name, Time: strconv.FormatFloat(result.Duration.Seconds(), 'f', 3, 64)}

		suite := junitSuite{Name: result.Name, Tests: "1"}
		switch result.Status {
		case testframework.Failed:
			test.Failure, suite.Failures = &junitMessage{result.Error}, "1"
		case testframework.Errored, testframework.Cancelled:
			test.Error, suite.Errors = &junitMessage{result.Error}, "1"
		case testframework.Skipped, testframework.Planned:
			test.Skipped, suite.Skipped = &junitMessage{result.Error}, "1"
		case testframework.Passed:
		}

		body, err := xml.Marshal(test)
		if err != nil {
			return fmt.Errorf("write j unit:\n%w", err)
		}

		suite.Body = string(body)
		report.Suites = append(report.Suites, suite)
	}

	data, err := xml.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("write j unit:\n%w", err)
	}

	if err := fileutils.MkdirAll(files, filepath.Dir(path)); err != nil {
		return fmt.Errorf("write j unit:\n%w", err)
	}

	if err := fileutils.WriteFile(files, path, append([]byte(xml.Header), data...), fileperms.PrivateFile); err != nil {
		return fmt.Errorf("write aggregated JUnit report:\n%w", err)
	}

	return nil
}

func needsRunOutcome(status testframework.Status, native []junitSuite) (bool, error) {
	if len(native) == 0 || status == testframework.Errored || status == testframework.Cancelled {
		return true, nil
	}

	if status == testframework.Failed {
		hasFailure, err := hasNativeFailure(native)

		return !hasFailure, err
	}

	return false, nil
}

func hasNativeFailure(suites []junitSuite) (bool, error) {
	for _, suite := range suites {
		var parsed struct {
			Cases []struct {
				Failure *struct{} `xml:"failure"`
				Error   *struct{} `xml:"error"`
			} `xml:"testcase"`
		}
		if err := xml.Unmarshal([]byte("<testsuite>"+suite.Body+"</testsuite>"), &parsed); err != nil {
			return false, fmt.Errorf("inspect native JUnit outcomes:\n%w", err)
		}

		for _, testcase := range parsed.Cases {
			if testcase.Failure != nil || testcase.Error != nil {
				return true, nil
			}
		}
	}

	return false, nil
}

func readJUnit(files opctx.FS, path string) ([]junitSuite, error) {
	if path == "" {
		return nil, nil
	}

	data, err := fileutils.ReadFile(files, path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("framework reported a missing JUnit file %#q", path)
	}

	if err != nil {
		return nil, fmt.Errorf("read j unit:\n%w", err)
	}

	var root struct {
		XMLName xml.Name `xml:""`
	}
	if err := xml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("invalid JUnit report %#q:\n%w", path, err)
	}

	switch root.XMLName.Local {
	case "testsuites":
		var report junitSuites
		if err := xml.Unmarshal(data, &report); err != nil {
			return nil, fmt.Errorf("read j unit:\n%w", err)
		}

		return report.Suites, nil
	case "testsuite":
		var suite junitSuite
		if err := xml.Unmarshal(data, &suite); err != nil {
			return nil, fmt.Errorf("read j unit:\n%w", err)
		}

		return []junitSuite{suite}, nil
	default:
		return nil, fmt.Errorf("unexpected JUnit root %#q in %#q", root.XMLName.Local, path)
	}
}
