# Automate artifact tests

Use the same `component test` and `image test` commands in CI as locally.
Preserve the command's exit status, save an aggregate JUnit report, and read the
retained `results.json` for detailed outcomes. Do not infer success from the
presence of a report, or from the last line printed by a native framework.

## Give each invocation its own output root

This Bash example runs the pytest check from
[Test an existing image](test-image.md). Set `PROJECT` and `IMAGE` to absolute
paths. Run the script from your CI checkout so `ci-artifacts` is retained there.
It requires `jq` in addition to the test's prerequisites.

```bash
set -euo pipefail

PROJECT="/absolute/path/to/image-test"
IMAGE="/absolute/path/to/azurelinux-4.x86_64.qcow2"
mkdir -p ci-artifacts
RUN_DIR="$(mktemp -d "$(pwd)/ci-artifacts/image-test.XXXXXX")"

status=0
azldev -C "$PROJECT" image test candidate \
  --test image-bytes --image-path "$IMAGE" --arch x86_64 \
  --work-dir "$RUN_DIR/work" \
  --junit-xml "$RUN_DIR/junit.xml" \
  >"$RUN_DIR/console.log" 2>&1 || status=$?
printf '%s\n' "$status" >"$RUN_DIR/exit-code"

shopt -s nullglob
reports=("$RUN_DIR"/work/tests/run-*/results.json)
if ((${#reports[@]} == 1)); then
  jq -r '.[] | [.name, .framework, .status, (.error // "")] | @tsv' \
    "${reports[0]}"
else
  printf 'Expected one results.json; found %s. See %s/console.log\n' \
    "${#reports[@]}" "$RUN_DIR" >&2
  if ((status == 0)); then
    status=1
  fi
fi

printf 'Retained artifacts: %s\n' "$RUN_DIR"
exit "$status"
```

A unique output root avoids accidentally consuming a previous run's report;
do not search the entire project's work directory for the newest JSON file.
The root must not overlap a `--source-dir` or `--lisa-dir` tree.
Relative azldev path flags resolve against `-C`, not the shell's working
directory; absolute output paths keep that distinction explicit.

Configure your CI's artifact-upload and JUnit-publishing steps to run **even when
the test command fails**, and publish `ci-artifacts/`. Do not use `|| true` around
the test job as a substitute for an always-run upload step.

Replace the test command for component testing, preserving the same output and
exit-status handling. The [standalone component example](test-component.md)
works in normal mode; no experimental lock-file flag is required.

## Interpret the exit status

For a normally completed invocation, azldev returns **0 on success and 1 on
error**. It does not propagate pytest's or another framework's native exit-code
taxonomy.

| Situation | CLI outcome | Structured outcome |
|-----------|-------------|--------------------|
| Assertions passed | 0 | `passed` |
| All collected cases skipped | 0 | `skipped` |
| One or more assertions failed | 1 | `failed`, unless an infrastructure error takes precedence |
| Setup, provisioning, execution, report, or cleanup error | 1 | `error` when results can be retained |
| No collected cases | 1 | `error`, not `skipped` |
| Gracefully handled cancellation | 1 | `cancelled` when results can be retained |
| Invalid config, unknown selector, unsupported combination | 1 | May fail before any report exists |
| Successful `--dry-run` | 0 | Printed `planned` results; no persisted test reports |

External termination, a crash, or an unwritable output directory can prevent
reports from being written. Treat missing or malformed reports as a job failure,
not an empty successful test run. Preserve the console log for these cases.
Even if all recorded cases passed, a nonzero process exit may indicate a later
reporting/cleanup failure; do not replace the exit status with a case count.

By default all selected tests are attempted. `--fail-fast` prevents subsequent
tests from starting after a failure; they appear as skipped results with a
reason, not successful executions. If CI requires at least one passing case,
enforce that separately: all-skipped is intentionally not a CLI error.

`-O json` formats the command's final result, including partial results returned
with an error. **It does not reserve stdout for JSON.** Native framework output
can share that stream, so redirecting it directly to `jq` is not a reliable
automation contract. Read the persisted file instead.

## Read normalized results and native diagnostics

An invocation normally retains:

```text
<work-dir>/tests/run-<unique-id>/
  results.json
  <test-name>/
    inputs.json
    python-environment.txt
    junit.xml
    ...native reports and logs...
```

Some files are absent if preparation failed before they could be produced.
The optional `--junit-xml` file is the aggregate report at the location you chose;
it does not replace individual test reports.

`results.json` is an array with one entry per selected named test, not one per
native test case. For example (paths and timings abbreviated):

```json
[
  {
    "name": "image-bytes",
    "framework": "pytest",
    "strategy": "host",
    "status": "passed",
    "duration": 5000000000,
    "outputDir": "/ci/work/tests/run-123/image-bytes",
    "junitPath": "/ci/work/tests/run-123/image-bytes/junit.xml",
    "cases": [
      {
        "name": "test_image_snapshot",
        "className": "test_image",
        "status": "passed",
        "nativeStatus": "passed",
        "duration": 400000000
      }
    ]
  }
]
```

JSON durations are numeric **nanoseconds**, both at run and case level. Run
duration includes framework preparation/execution work and is not necessarily
the sum of case durations. Optional `error`, case `message`, and `outputPath`
fields carry diagnostics when available. Additional orchestration-only entries,
such as `artifact-cleanup`, may appear; consumers must not assume every entry
is a configured framework test or contains `cases`.

For each input, `inputs.json` records the selected source (`path`, `local`, or
`lkg`) and available provenance. File inputs include the original path, private
snapshot path, and SHA-256. The snapshot path normally no longer exists after
execution; retain the original artifact separately if you need to reproduce the
run. OCI inputs use `ociImageId` rather than a file path.
For TMT VMs with configured bootstrap commands, `tmtBootstrapSource` identifies
the winning `distro-image:NAME`, `project-image:NAME`, or `test:NAME` setting.
The manifest's effective test configuration retains the resolved commands,
including an explicit empty list.

Use normalized statuses for CI policy, and native logs for diagnosis.
`python-environment.txt` records installed runtime distributions and available
direct-source metadata; TMT also retains `tmt-requirements.txt` and
`tmt-output.log`. Native output may contain environment details, credentials
printed by tests, or other sensitive data. Apply your CI's retention and access
policy rather than publishing all logs publicly.
