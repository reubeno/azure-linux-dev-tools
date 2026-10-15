// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testframework

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
)

type junitNode struct {
	XMLName  xml.Name    `xml:""`
	Tests    *int        `xml:"tests,attr"`
	Failures *int        `xml:"failures,attr"`
	Errors   *int        `xml:"errors,attr"`
	Skipped  *int        `xml:"skipped,attr"`
	Suites   []junitNode `xml:"testsuite"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string      `xml:"name,attr"`
	ClassName string      `xml:"classname,attr"`
	Time      string      `xml:"time,attr"`
	Failure   *junitIssue `xml:"failure"`
	Error     *junitIssue `xml:"error"`
	Skipped   *junitIssue `xml:"skipped"`
}

type junitIssue struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

// ReadJUnit reads native cases and checks any supplied summary counters against them.
// Partial cases remain available when a report's summary is contradictory.
func ReadJUnit(fs opctx.FS, path string) ([]CaseResult, error) {
	data, err := fileutils.ReadFile(fs, path)
	if err != nil {
		return nil, fmt.Errorf("read JUnit report %#q:\n%w", path, err)
	}

	var root junitNode

	decoder := xml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("invalid JUnit report %#q:\n%w", path, err)
	}

	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("invalid trailing JUnit content:\n%w", err)
		}

		switch value := token.(type) {
		case xml.CharData:
			if strings.TrimSpace(string(value)) != "" {
				return nil, errors.New("JUnit report has trailing non-XML content")
			}
		case xml.Comment, xml.ProcInst:
		default:
			return nil, errors.New("JUnit report must contain exactly one document")
		}
	}

	if root.XMLName.Local != "testsuite" && root.XMLName.Local != "testsuites" {
		return nil, fmt.Errorf("unexpected JUnit root %#q in %#q", root.XMLName.Local, path)
	}

	cases, err := root.normalized()
	if err != nil {
		return cases, fmt.Errorf("invalid JUnit report %#q:\n%w", path, err)
	}

	return cases, nil
}

func (node junitNode) normalized() ([]CaseResult, error) {
	cases := make([]CaseResult, 0, len(node.Cases))
	for _, native := range node.Cases {
		testcase, err := native.normalized()
		if err != nil {
			return cases, err
		}

		cases = append(cases, testcase)
	}

	for _, suite := range node.Suites {
		children, err := suite.normalized()

		cases = append(cases, children...)
		if err != nil {
			return cases, err
		}
	}

	counts := map[Status]int{}
	for _, testcase := range cases {
		counts[testcase.Status]++
	}

	for _, counter := range []struct {
		name   string
		value  *int
		actual int
	}{
		{"tests", node.Tests, len(cases)},
		{"failures", node.Failures, counts[Failed]},
		{"errors", node.Errors, counts[Errored]},
		{"skipped", node.Skipped, counts[Skipped]},
	} {
		if counter.value != nil && *counter.value != counter.actual {
			return cases, fmt.Errorf("JUnit '%s' count is %d but cases contain %d",
				counter.name, *counter.value, counter.actual)
		}
	}

	return cases, nil
}

func (native junitCase) normalized() (CaseResult, error) {
	result := CaseResult{Name: native.Name, ClassName: native.ClassName, Status: Passed, NativeStatus: "passed"}
	if strings.TrimSpace(native.Name) == "" {
		return result, errors.New("JUnit case requires a name")
	}

	if native.Time != "" {
		seconds, err := strconv.ParseFloat(native.Time, 64)
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 ||
			seconds >= float64(math.MaxInt64)/float64(time.Second) {
			return result, fmt.Errorf("invalid JUnit duration %#q", native.Time)
		}

		result.Duration = time.Duration(seconds * float64(time.Second))
	}

	outcomes := 0

	for _, outcome := range []struct {
		issue  *junitIssue
		status Status
		native string
	}{
		{native.Failure, Failed, "failure"}, {native.Error, Errored, "error"}, {native.Skipped, Skipped, "skipped"},
	} {
		if outcome.issue == nil {
			continue
		}

		outcomes++
		result.Status, result.NativeStatus = outcome.status, outcome.native

		result.Message = strings.TrimSpace(outcome.issue.Message)
		if result.Message == "" {
			result.Message = strings.TrimSpace(outcome.issue.Text)
		}
	}

	if outcomes > 1 {
		return result, fmt.Errorf("JUnit case %#q has contradictory outcomes", native.Name)
	}

	return result, nil
}
