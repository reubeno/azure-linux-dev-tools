// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package tmt wraps Fedora's Test Management Tool (tmt). The package's purpose is to
// hide the integration mechanics — venv installation, source cloning, argv construction,
// subprocess invocation, image-format coercion, results extraction, and graceful
// cancellation — behind a small, builder-style [Runner] API.
//
// Layered responsibility:
//
//   - This package: pure tmt mechanics. Takes an [opctx.Ctx] for filesystem and
//     subprocess access. Does not import azldev internals or projectconfig types.
//   - Caller (typically cmds/image): translates project configuration into a [RunSpec],
//     allocates a per-run directory (via the standard azldev workdir factory),
//     constructs a [Runner] with the appropriate builders, runs it, and translates
//     the [Result] back into the caller's preferred output shape (e.g.,
//     ImageTestResult rows for table/JSON formatting).
//
// Modeled after the [kiwi] sibling package, which wraps the kiwi-ng image builder.
//
// [kiwi]: github.com/microsoft/azure-linux-dev-tools/internal/utils/kiwi
package tmt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
)

const (
	// Bin is the tmt executable name as installed inside the per-suite venv.
	Bin = "tmt"

	// pythonProgram is the Python interpreter used to create the venv and run pip.
	pythonProgram = "python3"
	// gitProgram is the git executable used to clone tmt source repos.
	gitProgram = "git"
	// qemuImgProgram is used for image format detection and conversion.
	qemuImgProgram = "qemu-img"
	// qcow2Format is the qemu-img format string testcloud requires.
	qcow2Format = "qcow2"

	// commitSHALength is the canonical length of a full git SHA-1 hex commit hash.
	commitSHALength = 40

	// shortHashLen is the prefix length used in source-clone directory names so
	// repo+ref combinations (whose full hash would be 64 chars) yield reasonable
	// path names without collision risk in practice.
	shortHashLen = 16

	// SourceDirName is the subdirectory under the runner's base dir that holds
	// cached source clones, keyed by hash(url+ref).
	SourceDirName = "source"
	// VenvDirName is the subdirectory under the runner's base dir that holds
	// per-suite Python venvs.
	VenvDirName = "venv"

	// resultsFileMode is the permission used for files we write under the run dir.
	resultsFileMode = 0o600
	// dirMode is the permission used for directories we create.
	dirMode = 0o755

	// PluginsDirName is the subdirectory under each run dir where the runner emits
	// generated TMT_PLUGINS python files (currently just the cloud-init runcmds plugin).
	PluginsDirName = "tmt-plugins"

	// ConvertedImageFileName is the name the runner writes the converted qcow2 to,
	// inside the run dir, when the source image is in another format.
	ConvertedImageFileName = "image.qcow2"

	// OutputLogFileName is where the runner persists tmt's combined stdout+stderr.
	OutputLogFileName = "tmt-output.log"
	// JUnitFileName is the file written by tmt's junit reporter inside the run dir
	// when the caller requests it via [RunSpec.JUnitOutPath].
	JUnitFileName = "junit.xml"

	// DefaultBootTimeout is the wall-clock budget testcloud waits for a guest to come
	// up before failing. tmt's own default is 120s; we double it because (a) several
	// AzL images do non-trivial first-boot work and (b) developer hosts are often
	// loaded.
	DefaultBootTimeout = 300 * time.Second

	// DefaultCancelGracePeriod is how long we let the tmt subprocess unwind after we
	// forward a cancellation (SIGINT) to it before Go promotes the cancel to a SIGKILL.
	// Long enough for tmt's try/finally to run its `cleanup` step, which can take 30+
	// seconds when libvirt is slow.
	DefaultCancelGracePeriod = 60 * time.Second
)

// Errors returned by this package.
var (
	// ErrInvalidGitRef is returned when a git ref is not a 40-char hex commit SHA.
	ErrInvalidGitRef = errors.New("invalid git ref")

	// ErrMissingImage is returned when a [RunSpec] requires an image but none was
	// provided to the [Runner].
	ErrMissingImage = errors.New("no image-path was provided to the tmt runner")
)

// ProvisionHow identifies the tmt provision plugin to use ([tmt provision -h <how>]).
type ProvisionHow string

const (
	// ProvisionVirtual launches a libvirt/qemu VM via testcloud.
	// Requires a cloud-init-friendly image and the `provision-virtual` pip extra.
	ProvisionVirtual ProvisionHow = "virtual"
)

// pipExtraForProvisionHow maps each known tmt provision plugin to the pip extra that
// must be installed alongside `tmt` for that plugin to load. Used by the runner to
// auto-derive pip extras from the requested provisioner.
//
//nolint:gochecknoglobals // Effectively a constant; Go has no const maps.
var pipExtraForProvisionHow = map[ProvisionHow]string{
	ProvisionVirtual: "provision-virtual",
}

// Source identifies a git repository at a specific commit SHA. The runner reuses an
// existing clone when one matches the (url, ref) pair; otherwise it clones into a
// directory keyed by the hash of url+ref.
type Source struct {
	// GitURL is the URL of the git repository.
	GitURL string
	// Ref is the commit SHA to check out. Must be a 40-character hex commit hash.
	Ref string
}

// Validate checks that GitURL is non-empty and Ref is a 40-char hex SHA. The argument
// `context` is included in error messages so the caller can identify the source of a
// validation failure.
func (s Source) Validate(context string) error {
	if s.GitURL == "" {
		return fmt.Errorf("%s requires a git URL", context)
	}

	if s.Ref == "" {
		return fmt.Errorf("%s requires a ref", context)
	}

	if len(s.Ref) != commitSHALength {
		return fmt.Errorf("%w: %s expected %d hex characters, got %d: %#q",
			ErrInvalidGitRef, context, commitSHALength, len(s.Ref), s.Ref)
	}

	if _, err := hex.DecodeString(s.Ref); err != nil {
		return fmt.Errorf("%w: %s not a valid hex string: %#q",
			ErrInvalidGitRef, context, s.Ref)
	}

	return nil
}

// RunSpec describes one tmt run end-to-end: the source repo, plan name, context
// dimensions, step-scoped extra args, cloud-init runcmds, and provision plugin
// selection. It is the single input to [Runner.Run] beyond what's set on the runner
// itself (image path, verbosity, etc.).
type RunSpec struct {
	// Source is the git repository (and pinned commit) that contains the FMF plan
	// metadata to discover from.
	Source Source

	// Plan is the tmt plan name (must start with `/`). Required: this package only
	// supports running exactly one plan per [Runner.Run] invocation.
	Plan string

	// Context is a multi-valued map of tmt context dimensions, passed via `tmt -c k=v`.
	// Multi-valued because tmt's adjust: rules support set-membership constraints —
	// e.g., `distro = ["fedora-39", "fedora-40"]` causes the runner to emit
	// `-c distro=fedora-39 -c distro=fedora-40`.
	Context map[string][]string

	// PipExtras lists additional pip extras to install with tmt. The runner unions
	// this with the extra auto-derived from [ProvisionSpec.How] (e.g., `provision-virtual`
	// when how is `virtual`).
	PipExtras []string

	// RunExtraArgs are passed verbatim before the tmt `run` subcommand.
	RunExtraArgs []string
	// PlanExtraArgs are passed verbatim after `plan -n <plan>`.
	PlanExtraArgs []string
	// CloudInitRuncmds is a list of shell commands the runner injects into cloud-init's
	// runcmd phase via tmt's TMT_PLUGINS extension point. Each entry runs once on the
	// guest's first boot, before tmt's testcloud boot-complete probe fires.
	CloudInitRuncmds []string

	// Provision configures the tmt provision step.
	Provision ProvisionSpec

	// JUnitOutPath, when non-empty, instructs the runner to add `report -h junit
	// --file <path>` to the tmt argv. The caller is responsible for choosing the
	// path (typically inside the run dir).
	JUnitOutPath string
}

// ProvisionSpec is the subset of tmt's provision step this package projects.
// Anything not modeled here can be passed via [ProvisionSpec.ExtraArgs].
type ProvisionSpec struct {
	// How selects the tmt provision plugin (see [ProvisionHow]).
	How ProvisionHow

	// ExtraArgs are passed verbatim after `provision -h <how>`.
	ExtraArgs []string
}

// Result aggregates everything the runner discovered about a tmt run, regardless of
// whether the run succeeded. Fields are populated best-effort: a result is returned
// even when [Runner.Run] also returns a non-nil error (e.g., tmt failed before any
// test ran — Tests will be empty but VMName, ErrorClass, and ErrorSummary may still
// be populated).
type Result struct {
	// RunDir is the directory tmt wrote its run state into (the value passed to
	// `tmt run -i`).
	RunDir string

	// Tests is the per-test result rows extracted from tmt's results.yaml. Empty when
	// tmt did not reach the execute step.
	Tests []ResultEntry

	// VMName is the libvirt domain name that tmt assigned to the guest, when
	// extractable from the run log. Empty when the run never reached provision.
	VMName string

	// ErrorClass is a short stable classification of the proximate failure cause
	// (e.g., "boot-timeout", "ssh-timeout", "provision-failed"). Empty on success or
	// when no error occurred.
	ErrorClass string

	// ErrorSummary is a one-sentence human-readable proximate cause, extracted from
	// tmt's run log. Empty on success.
	ErrorSummary string
}

// ResultEntry is one row of [Result.Tests], corresponding to one test in tmt's
// results.yaml.
type ResultEntry struct {
	// Name is the framework-native test identifier (e.g., `/tests/smoke`).
	Name string
	// Status is the outcome — values follow tmt's vocabulary (pass, fail, error,
	// info, warn, skip).
	Status string
	// Duration is the wall-clock duration of the test, when reported. Zero when not.
	Duration time.Duration
	// OutputPath is a filesystem path to per-test output (typically tmt's
	// `execute/data/<test>/output.txt`).
	OutputPath string
}

// Runner constructs and runs a tmt invocation. Use [NewRunner] to construct one and
// the With* methods to configure it before calling [Runner.Run].
type Runner struct {
	ctx               opctx.Ctx
	baseDir           string
	imagePath         string
	verbose           bool
	bootTimeout       time.Duration
	cancelGracePeriod time.Duration
}

// NewRunner constructs a [Runner] rooted at baseDir. Source clones and per-suite venvs
// live as subdirectories of baseDir; per-run state lives wherever the caller chooses
// (passed to [Runner.Run] as runDir).
func NewRunner(ctx opctx.Ctx, baseDir string) *Runner {
	return &Runner{
		ctx:               ctx,
		baseDir:           baseDir,
		bootTimeout:       DefaultBootTimeout,
		cancelGracePeriod: DefaultCancelGracePeriod,
	}
}

// WithImage sets the image-under-test that the runner passes to tmt as `--image`.
// The path may be in any format `qemu-img` recognizes; the runner converts to qcow2
// inside the run dir if necessary.
func (r *Runner) WithImage(path string) *Runner {
	r.imagePath = path

	return r
}

// WithVerbose enables auto-injection of `-vv` into the tmt argv (when the user has
// not already supplied a verbosity flag in [RunSpec.RunExtraArgs]).
func (r *Runner) WithVerbose(verbose bool) *Runner {
	r.verbose = verbose

	return r
}

// WithBootTimeout overrides [DefaultBootTimeout]. Passed to tmt as the
// TMT_BOOT_TIMEOUT environment variable.
func (r *Runner) WithBootTimeout(d time.Duration) *Runner {
	r.bootTimeout = d

	return r
}

// WithCancelGracePeriod overrides [DefaultCancelGracePeriod]. The runner sends SIGINT
// (rather than SIGKILL) to the tmt subprocess on context cancellation so tmt can run
// its own try/finally cleanup; this is the upper bound on how long it has to do so.
func (r *Runner) WithCancelGracePeriod(d time.Duration) *Runner {
	r.cancelGracePeriod = d

	return r
}

// Run performs the full tmt invocation: clones source, sets up the venv, installs tmt
// with the appropriate extras, optionally converts the image to qcow2, generates a
// TMT_PLUGINS file when needed, builds the argv, executes tmt, and parses the run's
// output.
//
// runDir must be a directory the caller has allocated (e.g., via the workdir
// factory); it does not need to exist yet — the runner ensures it. tmt's auxiliary
// state lands under filepath.Dir(runDir)/testcloud thanks to `--workdir-root`.
//
// The Result is populated even on error; the caller should always inspect it
// alongside the error to surface diagnostic detail.
func (r *Runner) Run(ctx context.Context, runDir string, spec RunSpec) (*Result, error) {
	if err := r.validateSpec(spec); err != nil {
		return nil, err
	}

	if r.imagePath == "" {
		return nil, ErrMissingImage
	}

	if err := fileutils.MkdirAll(r.ctx.FS(), runDir); err != nil {
		return nil, fmt.Errorf("failed to create run dir %#q:\n%w", runDir, err)
	}

	venvDir, err := r.setupVenv(ctx, spec)
	if err != nil {
		return nil, err
	}

	sourceDir, err := r.ensureSourceClone(ctx, &spec.Source)
	if err != nil {
		return nil, err
	}

	imagePath, err := ensureQcow2(ctx, r.ctx, r.imagePath, runDir)
	if err != nil {
		return nil, err
	}

	if err := r.maybeWritePluginsDir(runDir, spec); err != nil {
		return nil, err
	}

	tmtBin := filepath.Join(venvDir, "bin", Bin)
	argv := buildArgv(r.verbose, spec, runDir, imagePath)
	subprocessEnv := r.buildSubprocessEnv(runDir, spec)

	slog.Info("Running tmt",
		slog.String("bin", tmtBin),
		slog.Any("args", argv))

	runErr := r.executeTmt(ctx, tmtBin, argv, sourceDir, runDir, subprocessEnv)

	return r.assembleResult(runDir, spec.Plan, runErr), runErr
}

// validateSpec checks the parts of a [RunSpec] the runner cares about, deferring most
// validation to tmt itself.
func (r *Runner) validateSpec(spec RunSpec) error {
	if err := spec.Source.Validate("RunSpec.Source"); err != nil {
		return err
	}

	if spec.Plan == "" {
		return errors.New("RunSpec.Plan is required")
	}

	return nil
}

// ensureSourceClone clones the configured repo at the configured ref into a directory
// keyed by hash(git-url + ref). Idempotent across runs: an already-checked-out repo
// at the right ref is reused.
func (r *Runner) ensureSourceClone(ctx context.Context, source *Source) (string, error) {
	repoDir := r.sourceCloneDir(source)

	exists, err := fileutils.DirExists(r.ctx.FS(), repoDir)
	if err != nil {
		return "", fmt.Errorf("cannot check tmt source dir at %#q:\n%w", repoDir, err)
	}

	if exists {
		slog.Info("Reusing existing tmt source clone",
			slog.String("path", repoDir),
			slog.String("ref", source.Ref))

		return repoDir, nil
	}

	if err := fileutils.MkdirAll(r.ctx.FS(), filepath.Dir(repoDir)); err != nil {
		return "", fmt.Errorf("failed to create tmt source parent dir:\n%w", err)
	}

	slog.Info("Cloning tmt source repo",
		slog.String("url", source.GitURL),
		slog.String("ref", source.Ref),
		slog.String("dest", repoDir))

	if err := runGitCommand(ctx, r.ctx, "", "clone", "--quiet", source.GitURL, repoDir); err != nil {
		return "", fmt.Errorf("git clone of %#q failed:\n%w", source.GitURL, err)
	}

	if err := runGitCommand(ctx, r.ctx, repoDir, "checkout", "--quiet", source.Ref); err != nil {
		return "", fmt.Errorf("git checkout of %#q failed:\n%w", source.Ref, err)
	}

	return repoDir, nil
}

// sourceCloneDir returns the directory the runner caches a clone of (source.GitURL
// at source.Ref) into. The directory name is derived from a stable hash so callers
// don't accidentally collide across different URLs that happen to share the same
// ref's first hex digits.
func (r *Runner) sourceCloneDir(source *Source) string {
	key := sha256.Sum256([]byte(source.GitURL + "\x00" + source.Ref))
	keyHex := hex.EncodeToString(key[:])[:shortHashLen]

	return filepath.Join(r.baseDir, SourceDirName, keyHex)
}

// runGitCommand runs `git <args...>` with the working directory set to dir (when
// non-empty). Used for the source-clone path.
func runGitCommand(ctx context.Context, octx opctx.Ctx, dir string, args ...string) error {
	gitCmd := exec.CommandContext(ctx, gitProgram, args...)
	gitCmd.Stdout = os.Stdout
	gitCmd.Stderr = os.Stderr

	if dir != "" {
		gitCmd.Dir = dir
	}

	wrapped, err := octx.Command(gitCmd)
	if err != nil {
		return fmt.Errorf("failed to create git command:\n%w", err)
	}

	if err := wrapped.Run(ctx); err != nil {
		return fmt.Errorf("git command failed:\n%w", err)
	}

	return nil
}

// setupVenv creates (or reuses) a Python venv keyed by hash(source url + ref) — so
// different ref pins (and thus different tmt versions installed by `pip install tmt`)
// get different venvs and don't fight each other. Installs tmt with the union of
// auto-derived and user-supplied pip extras.
func (r *Runner) setupVenv(ctx context.Context, spec RunSpec) (string, error) {
	venvDir := r.venvDirForSource(&spec.Source)
	venvPython := filepath.Join(venvDir, "bin", pythonProgram)

	exists, err := fileutils.Exists(r.ctx.FS(), venvPython)
	if err != nil {
		return "", fmt.Errorf("cannot check tmt venv at %#q:\n%w", venvDir, err)
	}

	if !exists {
		if err := createPythonVenv(ctx, r.ctx, venvDir); err != nil {
			return "", err
		}
	} else {
		slog.Info("Reusing existing tmt venv", slog.String("path", venvDir))
	}

	extras := MergedPipExtras(spec.Provision.How, spec.PipExtras)

	pipTarget := Bin
	if len(extras) > 0 {
		pipTarget = fmt.Sprintf("%s[%s]", Bin, strings.Join(extras, ","))
	}

	slog.Info("Installing tmt", slog.String("target", pipTarget))

	pipCmd := exec.CommandContext(ctx, venvPython, "-m", "pip", "install", "--quiet", pipTarget)
	pipCmd.Stdout = os.Stdout
	pipCmd.Stderr = os.Stderr

	wrapped, err := r.ctx.Command(pipCmd)
	if err != nil {
		return "", fmt.Errorf("failed to create pip install command:\n%w", err)
	}

	if err := wrapped.Run(ctx); err != nil {
		return "", fmt.Errorf("failed to install %s:\n%w", pipTarget, err)
	}

	return venvDir, nil
}

// venvDirForSource returns the venv directory the runner uses for a given source.
// Same hashing scheme as [Runner.sourceCloneDir]: 1:1 between source clone and venv.
func (r *Runner) venvDirForSource(source *Source) string {
	key := sha256.Sum256([]byte(source.GitURL + "\x00" + source.Ref))
	keyHex := hex.EncodeToString(key[:])[:shortHashLen]

	return filepath.Join(r.baseDir, VenvDirName, keyHex)
}

// VenvDirForSpec returns the venv directory the runner would use for a given spec,
// without performing any side-effects. Callers need this for the fallback Clean path
// (which needs to know which venv's tmt binary to invoke).
func (r *Runner) VenvDirForSpec(spec RunSpec) string {
	return r.venvDirForSource(&spec.Source)
}

// createPythonVenv creates a new Python venv at venvDir using `python3 -m venv`.
func createPythonVenv(ctx context.Context, octx opctx.Ctx, venvDir string) error {
	slog.Info("Creating Python venv", slog.String("path", venvDir))

	venvCmd := exec.CommandContext(ctx, pythonProgram, "-m", "venv", venvDir)
	venvCmd.Stdout = os.Stdout
	venvCmd.Stderr = os.Stderr

	wrapped, err := octx.Command(venvCmd)
	if err != nil {
		return fmt.Errorf("failed to create venv command:\n%w", err)
	}

	if err := wrapped.Run(ctx); err != nil {
		return fmt.Errorf("failed to create Python venv at %#q:\n%w", venvDir, err)
	}

	return nil
}

// MergedPipExtras returns the union of the extra auto-derived from [ProvisionHow] and
// the caller-supplied list, deduplicated and stably ordered. Exposed for testability;
// see also [PipExtraForProvisionHow].
func MergedPipExtras(how ProvisionHow, extras []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(extras)+1)

	add := func(extra string) {
		if extra == "" {
			return
		}

		if _, ok := seen[extra]; ok {
			return
		}

		seen[extra] = struct{}{}

		out = append(out, extra)
	}

	if extra, ok := pipExtraForProvisionHow[how]; ok {
		add(extra)
	}

	for _, extra := range extras {
		add(extra)
	}

	sort.Strings(out)

	return out
}

// PipExtraForProvisionHow returns the pip extra that must be installed alongside tmt
// for the given provision plugin to load (e.g., `provision-virtual` for `virtual`).
// Returns ok=false for an unknown how.
func PipExtraForProvisionHow(how ProvisionHow) (string, bool) {
	extra, ok := pipExtraForProvisionHow[how]

	return extra, ok
}

// maybeWritePluginsDir generates a TMT_PLUGINS-loadable Python file under runDir when
// the spec has cloud-init runcmds; otherwise returns nil. The file appends each
// runcmd to tmt's TESTCLOUD_WORKAROUNDS list, which tmt funnels into cloud-init's
// runcmd before testcloud's HTTP boot-complete probe runs.
func (r *Runner) maybeWritePluginsDir(runDir string, spec RunSpec) error {
	if len(spec.CloudInitRuncmds) == 0 {
		return nil
	}

	pluginDir := filepath.Join(runDir, PluginsDirName)
	if err := os.MkdirAll(pluginDir, dirMode); err != nil {
		return fmt.Errorf("failed to create tmt plugin dir %#q:\n%w", pluginDir, err)
	}

	body, err := renderRuncmdsPlugin(spec.CloudInitRuncmds)
	if err != nil {
		return fmt.Errorf("failed to render tmt plugin:\n%w", err)
	}

	pluginFile := filepath.Join(pluginDir, "azldev_cloud_init_runcmds.py")
	if err := os.WriteFile(pluginFile, []byte(body), resultsFileMode); err != nil {
		return fmt.Errorf("failed to write tmt plugin %#q:\n%w", pluginFile, err)
	}

	slog.Info("Wrote tmt cloud-init runcmds plugin",
		slog.String("path", pluginFile),
		slog.Int("runcmd-count", len(spec.CloudInitRuncmds)))

	return nil
}

// buildSubprocessEnv constructs the environment for the tmt subprocess. We start from
// the host environment so user-managed knobs (HOME, PATH, libvirt creds, etc.) flow
// through, then layer on TMT_BOOT_TIMEOUT and (when applicable) TMT_PLUGINS.
func (r *Runner) buildSubprocessEnv(runDir string, spec RunSpec) []string {
	env := append([]string(nil), os.Environ()...)
	env = append(env, fmt.Sprintf("TMT_BOOT_TIMEOUT=%d", int(r.bootTimeout.Seconds())))

	if len(spec.CloudInitRuncmds) > 0 {
		env = append(env, "TMT_PLUGINS="+filepath.Join(runDir, PluginsDirName))
	}

	return env
}

// executeTmt invokes the tmt binary with the constructed argv, teeing stdout/stderr
// into <runDir>/<OutputLogFileName> while still streaming live to the caller's
// stdout/stderr. On context cancellation it sends SIGINT to give tmt's own
// try/finally a chance to run before Go promotes the cancel to a SIGKILL.
func (r *Runner) executeTmt(
	ctx context.Context,
	tmtBin string, argv []string,
	sourceDir, runDir string, env []string,
) error {
	outputLogPath := filepath.Join(runDir, OutputLogFileName)

	outputFile, err := os.OpenFile(outputLogPath,
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC, resultsFileMode)
	if err != nil {
		return fmt.Errorf("failed to open tmt output log %#q:\n%w", outputLogPath, err)
	}

	defer func() { _ = outputFile.Close() }()

	tmtCmd := exec.CommandContext(ctx, tmtBin, argv...)
	tmtCmd.Stdout = io.MultiWriter(os.Stdout, outputFile)
	tmtCmd.Stderr = io.MultiWriter(os.Stderr, outputFile)
	tmtCmd.Dir = sourceDir
	tmtCmd.Env = env

	gracePeriod := r.cancelGracePeriod

	tmtCmd.Cancel = func() error {
		slog.Warn("Forwarding cancellation to tmt subprocess; allowing time for graceful unwind",
			slog.Duration("wait-delay", gracePeriod))

		return tmtCmd.Process.Signal(os.Interrupt)
	}
	tmtCmd.WaitDelay = gracePeriod

	wrapped, err := r.ctx.Command(tmtCmd)
	if err != nil {
		return fmt.Errorf("failed to create tmt command:\n%w", err)
	}

	if err := wrapped.Run(ctx); err != nil {
		return fmt.Errorf("tmt invocation failed:\n%w", err)
	}

	return nil
}

// assembleResult builds the [Result] struct from on-disk artifacts after a tmt run.
// runErr is the error returned by the tmt invocation (or nil on success); when
// non-nil, ErrorClass and ErrorSummary are populated from the run log.
func (r *Runner) assembleResult(runDir, plan string, runErr error) *Result {
	result := &Result{RunDir: runDir}

	tests, err := parseRunResults(r.ctx.FS(), runDir, plan)
	if err != nil {
		slog.Warn("Failed to extract structured tmt results",
			slog.String("err", err.Error()))
	}

	result.Tests = tests
	result.VMName = vmNameFromTmtLog(runDir)

	if runErr != nil {
		result.ErrorClass, result.ErrorSummary = classifyFailure(runDir, runErr)
	}

	return result
}

// ensureQcow2 returns a path to a qcow2 image, converting from another format if
// needed. The converted image is placed inside runDir so it is naturally cleaned up
// alongside other run artifacts and never reused across runs.
func ensureQcow2(ctx context.Context, octx opctx.Ctx, srcImage, runDir string) (string, error) {
	exists, err := fileutils.Exists(octx.FS(), srcImage)
	if err != nil {
		return "", fmt.Errorf("cannot check image at %#q:\n%w", srcImage, err)
	}

	if !exists {
		return "", fmt.Errorf("image not found at %#q", srcImage)
	}

	format, err := detectImageFormat(ctx, octx, srcImage)
	if err != nil {
		return "", err
	}

	if format == qcow2Format {
		slog.Info("Image is already qcow2; using as-is", slog.String("path", srcImage))

		return srcImage, nil
	}

	if err := fileutils.MkdirAll(octx.FS(), runDir); err != nil {
		return "", fmt.Errorf("failed to create run dir for image conversion:\n%w", err)
	}

	dst := filepath.Join(runDir, ConvertedImageFileName)

	slog.Info("Converting image to qcow2",
		slog.String("from", srcImage),
		slog.String("from-format", format),
		slog.String("to", dst))

	convertCmd := exec.CommandContext(ctx, qemuImgProgram, "convert",
		"-O", qcow2Format, srcImage, dst)
	convertCmd.Stdout = os.Stdout
	convertCmd.Stderr = os.Stderr

	wrapped, err := octx.Command(convertCmd)
	if err != nil {
		return "", fmt.Errorf("failed to create qemu-img command:\n%w", err)
	}

	if err := wrapped.Run(ctx); err != nil {
		return "", fmt.Errorf("qemu-img convert failed:\n%w", err)
	}

	return dst, nil
}

// detectImageFormat shells out to `qemu-img info --output=json` and returns the
// reported format string (e.g., "qcow2", "raw", "vpc" for VHD).
func detectImageFormat(ctx context.Context, octx opctx.Ctx, path string) (string, error) {
	infoCmd := exec.CommandContext(ctx, qemuImgProgram, "info", "--output=json", path)

	var stdout strings.Builder

	infoCmd.Stdout = &stdout
	infoCmd.Stderr = os.Stderr

	wrapped, err := octx.Command(infoCmd)
	if err != nil {
		return "", fmt.Errorf("failed to create qemu-img info command:\n%w", err)
	}

	if err := wrapped.Run(ctx); err != nil {
		return "", fmt.Errorf("qemu-img info failed:\n%w", err)
	}

	var info struct {
		Format string `json:"format"`
	}

	if err := json.Unmarshal([]byte(stdout.String()), &info); err != nil {
		return "", fmt.Errorf("failed to parse qemu-img info output:\n%w", err)
	}

	if info.Format == "" {
		return "", fmt.Errorf("qemu-img info did not report a format for %#q", path)
	}

	return info.Format, nil
}

// Clean is the fallback cleanup path: invokes `tmt clean -i <runDir>` against the
// per-suite venv's tmt binary. The common-case cleanup is tmt's own `cleanup` step
// (enabled by `--all` in the argv), which runs inside tmt's `try/finally` even on
// failure. Clean is for the residual case where the tmt subprocess was killed before
// reaching that finally — e.g., SIGKILL, a Python segfault, or anything else that
// bypassed Go's deferreds in the parent.
//
// The caller is responsible for passing a fresh, time-bounded ctx so cleanup survives
// a cancelled parent context — without that, a Ctrl-C would propagate to the cleanup
// subprocess and leak the very VMs we're trying to reclaim.
func (r *Runner) Clean(ctx context.Context, runDir, venvDir string) error {
	tmtBin := filepath.Join(venvDir, "bin", Bin)

	cleanCmd := exec.CommandContext(ctx, tmtBin, "clean", "guests", "-i", runDir)
	cleanCmd.Stdout = os.Stdout
	cleanCmd.Stderr = os.Stderr
	cleanCmd.Env = append([]string(nil), os.Environ()...)

	wrapped, err := r.ctx.Command(cleanCmd)
	if err != nil {
		return fmt.Errorf("failed to create tmt clean command:\n%w", err)
	}

	if err := wrapped.Run(ctx); err != nil {
		return fmt.Errorf("tmt clean guests failed:\n%w", err)
	}

	return nil
}
