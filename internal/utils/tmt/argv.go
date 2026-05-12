// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tmt

import (
	"path/filepath"
	"sort"
	"strings"
)

// buildArgv constructs the step-scoped tmt argv:
//
//	tmt [-vv if verbose] [-c k=v ...] <run-extra-args>
//	    run --all --workdir-root <run-dir-parent> -i <run-dir>
//	    plan -n <plan> <plan-extra-args>
//	    provision [-h <how>] --image <image> <provision-extra-args>
//	    [report -h junit --file <junit-out>]
//
// `--all` (`-a`) tells tmt to run every step (discover/provision/prepare/execute/
// report/finish/cleanup) while still letting us customize the provision and report
// step options. This is critical: tmt's `cleanup` step is what calls `guest.stop()` +
// `guest.remove()` to tear down provisioned VMs, and tmt only runs steps that are
// "enabled". Without `--all`, naming `provision` (and optionally `report`) on the
// command line restricts the run to those steps only — `cleanup` never runs and VMs
// leak. With `--all`, tmt's own `try/finally` in plan.go() guarantees the cleanup
// step runs even when an earlier step (e.g., provision/boot-timeout) fails.
//
// `--workdir-root <run-dir-parent>` re-roots all of tmt's auxiliary state — including
// testcloud's per-VM data dir, which would otherwise default to `/var/tmp/tmt/testcloud`
// — under the parent of our run dir. Combined with `-i <run-dir>`, this keeps every
// artifact tmt produces inside a single dir hierarchy the caller manages.
//
// When verbose is true and the user has not already supplied a verbosity flag in
// run-extra-args, we automatically prepend `-vv` so tmt's own verbose output flows
// through and lands in the run dir's tmt-output.log.
func buildArgv(verbose bool, spec RunSpec, runDir, imagePath string) []string {
	args := make([]string, 0, defaultArgvCapacity)

	if verbose && !runExtraArgsHaveVerbosity(spec.RunExtraArgs) {
		args = append(args, "-vv")
	}

	// Pre-run options: -c k=v (multi-valued; emit one per (key, value)).
	keys := make([]string, 0, len(spec.Context))
	for k := range spec.Context {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, k := range keys {
		for _, v := range spec.Context[k] {
			args = append(args, "-c", k+"="+v)
		}
	}

	args = append(args, spec.RunExtraArgs...)

	// `run` subcommand. -a makes tmt run all steps (so cleanup runs on failure).
	// --workdir-root re-roots tmt's auxiliary state into our work-dir tree so nothing
	// important lands in /var/tmp/tmt.
	args = append(args, "run", "-a", "--workdir-root", filepath.Dir(runDir), "-i", runDir)

	// `plan` step.
	args = append(args, "plan", "-n", spec.Plan)
	args = append(args, spec.PlanExtraArgs...)

	// `provision` step.
	args = append(args, "provision")

	if spec.Provision.How != "" {
		args = append(args, "-h", string(spec.Provision.How))
	}

	args = append(args, "--image", imagePath)
	args = append(args, spec.Provision.ExtraArgs...)

	// `report` step (only when JUnit output is requested).
	if spec.JUnitOutPath != "" {
		args = append(args, "report", "-h", "junit", "--file", spec.JUnitOutPath)
	}

	return args
}

// defaultArgvCapacity is a small initial-capacity hint for the argv slice; the actual
// final size depends on context, extra-args lists, etc. Used purely to avoid an early
// realloc.
const defaultArgvCapacity = 32

// runExtraArgsHaveVerbosity returns whether the user already supplied a -v / -vv /
// -vvv (or --verbose) flag in run-extra-args. We only auto-inject `-vv` when they
// haven't.
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
