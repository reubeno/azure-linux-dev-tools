# Testing guidelines

This document provides guidelines for writing and maintaining tests within the AZL Dev Preview project, with a special focus on using interface mocks via GoMock.

## Table of Contents

- [Unit Tests](#unit-tests)
- [Scenario Tests](#scenario-tests)
- [End-to-End Tests](#end-to-end-tests)
- [Mutation Testing](#mutation-testing)
- [Test Utilities](#test-utilities)
- [Mocking dependencies](#mocking-dependencies)
  - [Generating Mocks](#generating-mocks)
  - [Using Mocks in Tests](#using-mocks-in-tests)
    - [Best Practices](#best-practices)
    - [Basic Usage](#basic-usage)
    - [Setting Expectations](#setting-expectations)
    - [Setting up mock return values](#setting-up-mock-return-values)
    - [Testing Error Conditions](#testing-error-conditions)
    - [Mocks side effects](#mocks-side-effects)

## Unit Tests

- **CRITICAL**: Unit tests must NOT write to the real filesystem or spawn real external processes
- Use the test context facilities (`internal/global/testctx`) for in-memory filesystem access
- Tests should not assume any files in the real host filesystem are present
- Use table-driven tests where appropriate
- Test both success and failure cases
- Use meaningful test names that describe the scenario
- Leverage `testify` for assertions

## Scenario Tests

- Use the scenario testing framework in `scenario/`
- Test realistic user workflows
- Include both positive and negative test cases
- Use `mage scenarioUpdate` when test expectations change and snapshots need updating
- Use existing test helpers in `scenario/internal/cmdtest`

## End-to-End Tests

Tests tagged `e2e` are excluded from `mage scenario` and `mage all`. Run the
general end-to-end suite with:

```bash
mage e2e
```

### Azure Linux image build

`mage e2e -target=image` runs only the Azure Linux image-build test. It checks out the
current head of the Azure Linux `4.0` branch, records the resolved commit, builds
the current azldev binary, and invokes the real
`azldev image build 1p-vm-base-gen2` command in a dedicated privileged KIWI
container.

The checkout and KIWI work tree live entirely in the disposable builder
container. Only the resolved source revision and checked qcow2 are written to a
host artifact mount. This dedicated builder does not change the privileges of
ordinary scenario containers. Native framework tests use their own
purpose-specific containers.

### External test-framework contracts

The artifact-test execution path keeps these responsibilities separate:

| Package | Responsibility |
| --- | --- |
| Component/image commands | Select the subject and expose CLI options; invoke normal build services when requested. |
| `core/testexecution` | Plan selected tests, resolve inputs independently, invoke adapters, and persist combined results. |
| `core/artifacts` / `artifact` | Resolve local, explicit, or published inputs; retain provenance and builder-owned output records. |
| `testframework` | Shared execution, requirements, typed input, and result contracts; no framework installation logic. |
| `testsource` / `utils/pythonenv` | Prepare private source snapshots and managed Python environments. |
| `tmt`, `lisa`, `pytest` | Own native commands, framework setup, provisioning details, and native report interpretation. |

Do not move framework-specific flags or provisioning workarounds into command
packages, or make generic utilities depend on test orchestration.
Adapters return an explicit status/error pair checked by
`testframework.RunResult.Validate`: passed/skipped/planned outcomes have no error,
failed outcomes wrap `ErrTestFailed`, and infrastructure/cancellation outcomes
carry an error. The orchestrator preserves this classification rather than
reinterpreting framework exit codes or treating an unspecified status as success.
`ExecutionSpec.SSH` groups caller-owned connection settings. `RunRequest` has
no generic cache directory: only pytest's opt-out install mode consumes the
explicit caller-owned `PreparedPythonDir`. File artifacts and OCI image IDs
have distinct fields; materialized inputs cannot contain both.

The orchestrator owns per-invocation file snapshots and releases them after
native execution. Snapshots are read-only copies with digests, not hardlinks
to mutable build output. Acquisition, snapshot, and cleanup failures must reach
the returned results and reports. Native adapters never choose whether to build
or download an input.

Keep fast CLI/configuration scenarios separate from privileged VM integration.
The committed native harness builds on
[microsoft/azure-linux-dev-tools#381](https://github.com/microsoft/azure-linux-dev-tools/pull/381)
and [microsoft/azure-linux-dev-tools#382](https://github.com/microsoft/azure-linux-dev-tools/pull/382).
It reuses the scenario container plumbing, rather than a second harness.

| Command | Coverage |
| --- | --- |
| `mage e2e` | All E2E tests, including native framework contracts; requires Linux/Docker/network/KVM |
| `mage e2e -target=component` | Upstream component update/render workflows |
| `mage e2e -target=image` | Build `1p-vm-base-gen2` from current Azure Linux `4.0` and record the resolved commit |
| `mage e2e -target=frameworks` | Complete native TMT/LISA/pytest contract suite |
| `mage e2e -run='^TestNativeFrameworkContracts$/local'` | TMT host/container/SSH and pytest; no KVM required |
| `mage e2e -run='^TestNativeFrameworkContracts$/virtual/lisa'` | Focused LISA contracts; use `/virtual/tmt` for TMT |

The `-target` selector follows the image-build PR's single `mage e2e` entry point;
`-run` is an alternative for an exact Go test/subtest expression, not combined
with a named target. Native contracts use the ordinary `e2e` tag and run in the
dedicated **Native framework contracts** CI job on pull requests. The component
and image CI jobs select their own targets to avoid running the same tests twice.
They are not part of `mage all` or `mage scenario`. There is no quarantine switch.

Fixtures live in `scenario/testdata/frameworks/`. The local family exercises
component and image commands with explicit, reused local, and HTTP-published
inputs; pytest skip/assertion/setup-error/empty results; main's prepared-venv
location; TMT same-version candidate replacement; managed-container cancellation;
and a separate caller-owned SSH target that survives cancellation. The VM probe
checks firewall/readiness, SSH policy, package identity and executable behavior.
LISA runs component, image, and mixed pass/fail/skip cases on a real systemd/libvirt
host inside a privileged container with private namespaces. It also verifies empty
selection and a deliberately invalid candidate RPM, which must report a setup
error rather than a test assertion failure.

The native VM fixture is **x86_64 qcow2**. Other disk formats and architecture
selection are covered by unit contracts, not claimed as native matrix coverage.
Artifacts and timing observations are retained under `out/framework-e2e/` and
`out/tmt-virtual-e2e/`. Timings are diagnostic, not formal performance budgets.

The image-build E2E follows current upstream, while the framework suite builds
its regression image from the checked-in Azure Linux revision. Both use the same
builder container with checkout and work files kept entirely inside it.
For a shorter local loop, supply absolute `AZL_TMT_IMAGE`, `AZL_TMT_RPM`, and
`AZL_TMT_IMAGE_REVISION` together. `AZL_LISA_IMAGE` may additionally select an
already QEMU-compatible image. Without it, the suite uses azldev Image Customizer
to create a separate compatible copy; it never edits the original image.
No session-specific paths, private credentials, or unpublished fixture artifacts
are required.

Runtime pins live in `internal/tmt/requirements.txt`, aligned with the shared-pin
approach in [microsoft/azure-linux-dev-tools#384](https://github.com/microsoft/azure-linux-dev-tools/pull/384).
An integration harness must use the binary under review and these constraints,
not a separately built older implementation. Runtime environments are ephemeral;
verify `python-environment.txt` instead of depending on private venv paths.
All three adapters record installed distributions using Python's standard
metadata API, including prepared environments without pip. The LISA fixture
revision and pytest version are also checked in; dependency updates must pass
the native job.

TMT is temporarily pinned to the full Git commit
`8e05bafd17fb4d09d97d76a4bbd5677aee723f8b`, containing
[teemtee/tmt#5239](https://github.com/teemtee/tmt/pull/5239), rather than a
published release. Do not replace it with `main` or a moving tag. Advance the
constraint deliberately and run the native contracts; return to a published
release once it includes the required fixes. The native test verifies both
pip's PEP 610 `direct_url.json` and the executed runtime's recorded commit.

Remaining VM compatibility adaptations are limited to the following:

| Adaptation | Boundary and removal condition |
| --- | --- |
| Explicit TOML bootstrap commands | Appended through TMT's workaround list before readiness; replace when upstream exposes a supported guest-bootstrap hook. No hard-coded image commands. |
| `ssh_pwauth: false` | Merge into generated cloud-config after testcloud expands placeholders and before ISO construction; replace when upstream supports structured cloud-config overrides. Preserve all other fields. |
| Privileged artifact pull | Wrap only `GuestTestcloud.pull` transfer options with remote `sudo -n rsync`; remove when upstream provides privileged pull analogous to `push(superuser=True)`. Never use recursive chmod as fallback. |

`tmt-api-contracts.py.in` exercises the real pinned configuration renderer,
preservation of unrelated fields, scope of the pull wrapper, option forwarding,
and error propagation. The VM probe also retrieves root-owned `0600` files from
a `0700` directory and verifies guest ownership/modes after the execute pull.
The upstream waiting factory is checked directly, with no local polling patch.

Useful live checks include candidate package identity and behavior, unchanged
input files, guest cleanup on failure/cancellation, mixed-framework selection,
and per-case reports. Run host-mode tests only inside disposable systems.
For SSH testing, verify the caller-owned target remains reachable afterwards.

Measure framework setup, guest provisioning, native test execution, and cleanup
separately. Earlier TMT versions introduced four 30-second SSH-master waits in
a trivial successful SSH probe; the Git pin now contains the upstream
one-second polling fix, preserving the startup deadline. Testcloud's session
readiness service is distinct from SSH and needs TCP 10022 available in a
firewalld-managed guest; the fixture declares that policy in TOML.

A QEMU fixture must not depend on Hyper-V-only telemetry. A cloud image enabling
`reporting.telemetry.type: hyperv` can make cloud-init return degraded status on
QEMU even though SSH works. Build or explicitly customize a separate fixture
with a suitable reporting backend; do not suppress cloud-init failures or change
the user's original artifact to make an integration test green.

## Mutation Testing

Mutation testing measures how good the tests actually are (as opposed to mere line coverage) by
introducing small changes ("mutants") into the source and checking whether the test suite catches
them. A mutant that tests fail on is *killed*; one that passes is a *lived* mutant and points to a
real gap in test assertions. The project uses [gremlins](https://github.com/go-gremlins/gremlins),
pinned in `tools/gremlins`.

Mutation testing recompiles and reruns the tests for every mutant, so it's slower than unit tests;
running the whole repo (`./`) takes a few minutes. Scope it for quicker feedback:

```bash
# Scope to a single package.
mage mutation ./internal/rpm

# Scope to only the lines changed relative to a git ref (fastest; ideal on a branch).
mage mutationDiff main
```

Interpreting the output:

- **Test efficacy** is the percentage of `KILLED` mutants over `KILLED + LIVED`. Higher is better.
- **LIVED** mutants are the actionable signal: a change the tests did not detect.
- **NOT COVERED** mutants are in code with no test coverage at all.
- Some `LIVED` mutants are *equivalent* (e.g. `len(x) > 0` vs `>= 0` when the loop body no-ops on an
  empty slice) and cannot be killed. Inspect before "fixing".

The console output is filtered to just the actionable mutants (`LIVED` and `NOT COVERED`) plus the
summary totals; a full JSON report covering every mutant (including `KILLED`) is written to
`out/mutation-report.json` for tooling or detailed review.

Mutation testing only exercises **unit tests**. Scenario tests are gated behind the `scenario` build
tag and are not run by gremlins, so they never slow down a mutation run. The targets also exclude the
`scenario/` and `magefiles/` trees and generated mocks from the mutant set to avoid `NOT COVERED`
noise.

> **Note**: Timeout tuning is configured in `.gremlins.yaml` (`unleash.timeout-coefficient`, currently 100).
> On this repo's fast suites, gremlins' default is often too tight once recompilation is included, so mutants can be
> mis-reported as `TIMED OUT` (which gremlins counts as killed, inflating efficacy).
> If you run `gremlins` without the repo config, pass a higher `--timeout-coefficient` (e.g. 100) for accurate results.

## Test Utilities

- Create reusable test fixtures when appropriate
- Mock external dependencies appropriately. See [Mocking dependencies](#mocking-dependencies) for details
- The in-memory filesystem is available via `internal/global/testctx` for testing purposes

## Mocking dependencies

The AZL Dev Preview project uses [GoMock](https://github.com/golang/mock) to generate mock implementations of interfaces for testing purposes. These mocks allow for controlled testing of components that depend on interfaces without relying on concrete implementations.

Mock generation is handled through Go's built-in [`go:generate` functionality](https://pkg.go.dev/cmd/go#hdr-Generate_Go_files_by_processing_source). The project uses a pinned version of `mockgen` stored in the `tools/mockgen` directory to ensure consistent behavior across all environments.

### Generating Mocks

To generate new mocks for an interface, add a `go:generate` directive at the top of the file containing the interface. Example from `internal/global/opctx/interfaces.go`:

```go
//go:generate go tool -modfile=../../../tools/mockgen/go.mod mockgen -source=interfaces.go -destination=opctx_test/opctx_mocks.go -package=opctx_test --copyright_file=../../../LICENSE
```

The directive should:

1. Reference the mockgen tool using the relative path to the tool's go.mod file
2. Specify the source file containing the interfaces
3. Define the destination path for the generated mocks. Place the file inside a folder with the `_test` suffix to ensure it is ignored during test coverage reports
4. Set the package name for the generated mocks
5. Include a copyright file reference

Mocks are automatically generated as part of the build process when running any of the below commands:

```shell
mage generate
mage build
mage unit
go generate ./...
```

NOTE: `go test` will **NOT** run the `go:generate` directives.

### Using Mocks in Tests

#### Best Practices

1. **Don't overuse mocks**: Only mock interfaces that are external to the component under test
2. **Keep tests focused**: Test one unit of functionality at a time
3. **Use helper functions**: Create helper functions to set up common mock configurations
4. **Use table-driven tests**: When testing similar functionality with different inputs
5. **Follow existing patterns**: See examples in the codebase such as `internal/utils/externalcmd/externalcmd_test.go`

#### Basic Usage

Full documentation and examples for using GoMock can be found in the [GoMock documentation](https://github.com/golang/mock?tab=readme-ov-file#building-mocks). Below is a short set of usage examples from this project.

To use the generated mocks in your tests:

1. Import the mock package:

   ```go
   import (
       "go.uber.org/mock/gomock"
       "github.com/microsoft/azure-linux-dev-tools/internal/global/opctx/opctx_test"
   )
   ```

2. Create a controller and mock instances:

   ```go
   func TestMyFunction(t *testing.T) {
       // Create a new controller
       ctrl := gomock.NewController(t)

       // Create mock instances using the controller
       mockFileSystem := opctx_test.NewMockFileSystemFactory(ctrl)
       mockOSEnv := opctx_test.NewMockOSEnvFactory(ctrl)

       // Use the mocks in your test
       app := azldev.NewApp(mockFileSystem, mockOSEnv)

       // Set expectations, run test code, verify
   }
   ```

#### Setting Expectations

GoMock allows you to define expectations for method calls on your mock objects:

```go
// Expect DryRun() to be called any number of times and return false
mockDryRunnable.EXPECT().DryRun().AnyTimes().Return(false)

// Expect GetEventListener() to be called exactly once with any arguments
// and return a specific mock object
mockContext.EXPECT().GetEventListener().Times(1).Return(mockEventListener)
```

These expectations are automatically verified at the end of the test. If the expectations are not met, the test will fail. Similarly, if an unexpected method call is made on the mock, the test will also fail.

#### Setting up mock return values

You can configure a mock to return a specific value. This includes returning another pre-configured mock:

```go
// Must always create a controller for the mocks.
ctrl := gomock.NewController(t)

// Create a mock of the command factory interface.
mockCmdFactory := opctx_test.NewMockCommandFactory(ctrl)

// Create a mock of the command returned by the command factory.
mockCmd := opctx_test.NewMockCommand(ctrl)

// Set up the mock command's behaviour for its Run() method. Here it returns a nil error.
// The 'gomock.Any()' is an argument matcher making sure we won't fail the test regardless of the input.
mockCmd.EXPECT().Run(gomock.Any()).Return(nil)

// Set up the command factory to return the mock command when Command() is called.
mockCmdFactory.EXPECT().Command(gomock.Any()).Return(mockCmd, nil)

(...)

// Some test code using the mock command factory:
cmd, err := mockCmdFactory.Command("arbitrary input")
require.NoError(t, err)
require.Equal(t, mockCmd, cmd)

err = cmd.Run(ctx.Background())
require.NoError(t, err)

// GoMock will automatically verify that the expected calls were made on the mock.
```

#### Testing Error Conditions

Use mocks to simulate error conditions:

```go
mockCmd.EXPECT().Run(gomock.Any()).Return(errors.New("command error"))
```

#### Mocks side effects

You can define a function to be executed when a mock method is called, allowing you to simulate side effects:

```go
const testDir = "/test/dir"

// Must always create a controller for the mocks.
ctrl := gomock.NewController(t)

// Create a mock of a command we expect to run.
// The 'gomock.Any()' is an argument matcher making sure we won't fail the test regardless of the input.
testFS := afero.NewMemMapFs()
mockCmd := opctx_test.NewMockCommand(ctrl)
mockCmd.EXPECT().Run(gomock.Any()).DoAndReturn(func(_ context.Context) error {
    // Simulate some side effect. Creating a directory in this case.
    err := fileutils.MkdirAll(testFS, testDir)
    require.NoError(t, err)

    return nil
})

...
```
