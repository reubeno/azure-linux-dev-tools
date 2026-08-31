# RPM Repos & Repo Sets

This page explains how azldev models RPM repositories: where they're defined, how reusable layout templates produce families of related repos, how distro versions select build inputs, and how named profiles compare published inventories.

For field-level reference documentation, see:

- [Resources](../reference/config/resources.md) — `[resources.rpm-repos.*]`, `[resources.rpm-repo-set-templates.*]`, `[resources.rpm-repo-sets.*]`
- [Distros — Inputs](../reference/config/distros.md#inputs) — `[distros.<name>.versions.<v>.inputs]`

## Why?

Historically, RPM repository URLs were hard-coded in `mock` template files (`*.tpl`) and inside `.kiwi` files for local builds. That had several costs:

- the **same** URL was duplicated across mock templates and kiwi configs;
- swapping between staging and production trees required editing checked-in files;
- there was no canonical place to list what repos a build pulls from, so `azldev config dump` could not answer "what repos does this distro version use?";
- conventional layouts (the Standard Azure Linux layout, the Koji dist-repo layout) had no first-class representation, so each consumer had to know the layout itself.

The configuration described here lets you define each repo (or layout) **once** in TOML and select it per-distro-version.

## Concepts

There are three layers, from concrete to abstract:

```
                     ┌─────────────────────────┐
                     │  rpm-repo-set-templates │  reusable layouts (e.g. azl-standard)
                     └────────────┬────────────┘
                                  │ instantiated by
                                  ▼
┌──────────────┐         ┌─────────────────────┐
│   rpm-repos  │         │     rpm-repo-sets   │  named bundle: layout + base-uri + gpg-key
└──────┬───────┘         └──────────┬──────────┘
       │                            │ expands at validation time
       └─────────────┬──────────────┘
                     ▼
        union namespace of repos available to
        [distros.<d>.versions.<v>.inputs]
```

### `rpm-repos`

A flat map of named RPM repositories. Each entry has a `base-uri` (or `metalink`), GPG configuration, and optional arch filter. Use this for one-off repos that don't fit a layout template — e.g., a single upstream Fedora `Everything` repo.

### `rpm-repo-set-templates`

A *layout* template: an ordered list of sub-repos described as `(name, kind, subpath)`, where `subpath` is relative to the layout's base URL and may contain `$basearch`. Templates have **no** URLs of their own — they are pure structure.

Sub-repo `kind` is a classification (`binary`, `debug`, or `source`) describing what the sub-repo carries. It's diagnostic — currently used only in synthesized descriptions — but reserved as a stable label for future filtering features.

Two templates ship out of the box:

- **`azl-standard`** — the Azure Linux PMC layout: `base`, `builddeps` (the `rpm-sdk` publish channel), and `microsoft`, each with binary, debuginfo, and SRPM repositories.
- **`koji-dist-repo`** — Koji dist-repo layout: per-arch binary tree at `<arch>/`, parallel debuginfo tree at `<arch>/debug/`, and a single `src/` tree for SRPMs.

You can define your own templates if your deployment uses a different layout; they're just regular config that any project can author.

Each template row can list `publish-channels`. `azldev repo compare` uses that
mapping to verify that package placement agrees with the fully resolved component,
package-group, and package-specific publish policy.

### `rpm-repo-sets`

A named *instantiation* of a template: it pairs a template with a deployment-specific `base-uri`, `name-prefix`, and shared GPG configuration. At validation time each set expands into one synthesized `rpm-repos` entry per included sub-repo, keyed `<name-prefix><subrepo.name>`.

By default every sub-repo declared in the referenced template is instantiated. To take only some of them, list their names explicitly:

```toml
subrepos = ["base", "sdk"]   # allowlist; everything else is dropped
```

You can also scope the entire set to specific architectures:

```toml
arches = ["x86_64"]
```

### Distro version inputs

Each distro version declares **per-use-case** input lists. Each entry references either an individual repo (`repo = "..."`) or a repo set (`set = "..."`); set entries expand inline at validation time:

```toml
[distros.mydistro.versions.'4.0'.inputs]
rpm-build = [
    { repo = "fedora-43-everything" },   # individual repo
    { set  = "azl4-prod" },              # expanded into N synthesized repos
]
image-build = [
    { repo = "fedora-43-everything" },
    { set  = "azl4-prod" },
]
```

The effective list is each entry expanded in declaration order. Duplicates (whether from a direct `repo` entry or two sets whose expansions overlap) are rejected with a clear error rather than silently de-duped.

Why split RPM-build vs image-build? They have different security envelopes:

- mock evaluates `gpg-key` URIs *inside* the chroot, so a local file path is invisible. azldev rejects local `gpg-key` values for `rpm-build` repos.
- kiwi runs on the host, so any URI form works for `image-build`.

### Repository comparisons

`[resources.rpm-repo-comparisons.<name>]` records left/right repo sets,
architectures, routing validation, and optional latest-only filtering. Run it with:

```sh
azldev repo compare --comparison azl4-koji-vs-preview
```

The report is grouped by RPM package name. Each differing package contains its
complete relevant left and right NEVR inventories, with RPM architectures,
logical publication channels, and repository-architecture coverage. A compact
package summary identifies inventory, architecture, content, duplicate
publication, routing, and `noarch` replication differences.

Inventory summaries are directional. `added-in-right` means that one or more
NEVR, kind, and RPM-architecture identities exist only on the right;
`missing-from-right` means that identities from the left are absent on the
right. A package with changes in both directions carries both statuses. These
statuses also cover package names that are entirely absent from one side.

Set `ignore-older-added-in-right = true` when historical builds retained on the
right should not count as additions. A right-only identity is ignored only when
the left contains the same package name, artifact kind, and RPM architecture at
a strictly newer EVR. It remains in the displayed right inventory when another
difference keeps that package in the report. The Koji-versus-PMC profiles use
this option so older published builds do not obscure packages added to preview.

When publish routing is enabled, every artifact includes its resolved expected
publish channel. This annotation is also present for an `unrouted` side such as
Koji, showing where its packages should be published even though placement in
that source repository is not treated as a routing error.

JSON exposes the canonical nested report, including aggregate package counts and
the exact repomd snapshots used. Markdown renders an overall summary followed by
one package heading with left and right inventory tables. Table and CSV output
contain one summary row per package name with the package statuses and left/right
NEVRs. Identical package names are omitted.

All repomd files are fetched before primary metadata, and no ambient dnf cache is
used. If two matching package records use different checksum algorithms, the
package summary records `content-comparison-skipped` instead of comparing unlike
digests.

Set `skip-checksum-comparison = true` for comparisons where expected
transformations change complete-RPM bytes, such as unsigned Koji artifacts versus
signed PMC artifacts. This disables checksum, size, and cross-architecture
`noarch` content checks, but retains inventory, duplicate-placement, missing
`noarch` replica, and publish-routing findings.

Identical `noarch` packages replicated across every architecture repository in the
same logical sub-repo are expected and are not duplicates. The report does flag a
`noarch` package missing from an architecture, replicas with different content, or
the same `noarch` NEVRA published into multiple logical channels.

Comparisons automatically restrict themselves to artifact kinds available on both
sides. Mark an unrouted set such as a Koji build tag with `unrouted = true`; its
packages participate in inventory/content comparison, but physical channel routing
is not checked. `latest-only = true` selects the latest EVR independently on each
side and within each physical sub-repo, package name, architecture, and kind.

## Load-time vs use-time

Expansion is deterministic and happens once during `ProjectConfig.Validate()`, after **all** config files (project, user, `--config-file` extras) have been merged. This means:

- a later config file can override an earlier set's `base-uri` or `gpg-key`;
- the merged config in `azldev config dump` is round-trippable: what you see is what you authored, and re-loading the dumped TOML produces the same effective state;
- consumers (mock, kiwi) call `ResourcesConfig.EffectiveRpmRepos()` to get the flat resolved map without re-parsing layouts.

`gpg-key` paths follow the usual rule: bare paths are resolved relative to the directory of the file that defines them, then re-emitted as a `file` URI. URI-shaped values (an `http` or `https` URI, or a `file` URI with an absolute path) are passed through unchanged.

## End-to-end example

```toml
# ─────────────────────────────────────────────────────────────────────────────
# Layout template: the base/sdk/microsoft × binary/debug/source PMC matrix.
# (Already shipped under defaultconfigs/content/defaults.toml as `azl-standard`;
# reproduced here for clarity. Project files can override it by re-defining the
# same name.)
# ─────────────────────────────────────────────────────────────────────────────
[resources.rpm-repo-set-templates.azl-standard]
description = "Standard base/sdk/microsoft x binary/debug/source PMC layout"
subrepos = [
    { name = "base",          kind = "binary", subpath = "base/$basearch",                publish-channels = ["rpm-base"] },
    { name = "base-debug",    kind = "debug",  subpath = "base/debuginfo/$basearch",      publish-channels = ["rpm-base-debuginfo"] },
    { name = "base-src",      kind = "source", subpath = "base/srpms",                    publish-channels = ["rpm-base-srpm"] },
    { name = "sdk",           kind = "binary", subpath = "builddeps/$basearch",           publish-channels = ["rpm-sdk"] },
    { name = "sdk-debug",     kind = "debug",  subpath = "builddeps/debuginfo/$basearch", publish-channels = ["rpm-sdk-debuginfo"] },
    { name = "sdk-src",       kind = "source", subpath = "builddeps/srpms",               publish-channels = ["rpm-sdk-srpm"] },
    { name = "microsoft",     kind = "binary", subpath = "microsoft/$basearch",           publish-channels = ["rpm-microsoft"] },
    { name = "microsoft-debug", kind = "debug", subpath = "microsoft/debuginfo/$basearch", publish-channels = ["rpm-microsoft-debuginfo"] },
    { name = "microsoft-src", kind = "source", subpath = "microsoft/srpms",               publish-channels = ["rpm-microsoft-srpm"] },
]

# A custom layout for a Koji dist-repo (per-arch binary + debug trees plus a
# `src/` tree). Also shipped as a built-in template.
[resources.rpm-repo-set-templates.koji-dist-repo]
description = "Koji dist-repo layout: per-arch binary + debug trees plus a src/ tree"
subrepos = [
    { name = "binary", kind = "binary", subpath = "$basearch" },
    { name = "debug",  kind = "debug",  subpath = "$basearch/debug" },
    { name = "src",    kind = "source", subpath = "src" },
]

# ─────────────────────────────────────────────────────────────────────────────
# Repo sets: concrete deployments of the layouts above.
# ─────────────────────────────────────────────────────────────────────────────

# Production AzL 4.0 build inputs. Take only the binary + source sub-repos.
[resources.rpm-repo-sets.azl4-prod]
description = "Production build inputs"
template    = "azl-standard"
base-uri    = "https://example.com/azl4"
name-prefix = "azl4-"
gpg-key     = "https://example.com/keys/RPM-GPG-KEY"
subrepos    = ["base", "base-src", "sdk", "sdk-src"]

# Same layout, different mirror — for staging / pre-prod testing.
[resources.rpm-repo-sets.azl4-staging]
description = "Staging mirror"
template    = "azl-standard"
base-uri    = "https://staging.example.com/azl4"
name-prefix = "azl4-staging-"
gpg-key     = "https://example.com/keys/RPM-GPG-KEY"

# A Koji dist-repo we pull build deps from.
[resources.rpm-repo-sets.koji-build-deps]
description = "Build deps from the latest dist-repo"
template    = "koji-dist-repo"
base-uri    = "https://kojipkgs.example.com/repos/dist-azl4-build/latest"
name-prefix = "koji-build-deps-"
gpg-key     = "/etc/pki/build.gpg"   # local file: ok for image-build only

# ─────────────────────────────────────────────────────────────────────────────
# Standalone repo: an upstream Fedora source we use for spec sources.
# ─────────────────────────────────────────────────────────────────────────────
[resources.rpm-repos.fedora-43-everything]
description = "Fedora 43 Everything (binary)"
base-uri    = "https://example.com/releases/43/Everything/$basearch/os/"
gpg-key     = "https://example.com/keys/RPM-GPG-KEY-fedora-43-primary"

# ─────────────────────────────────────────────────────────────────────────────
# Distro version: select which inputs each use-case sees.
# ─────────────────────────────────────────────────────────────────────────────
[distros.mydistro.versions.'4.0'.inputs]
# RPM build: pull from production AzL + Fedora upstream.
rpm-build = [
    { repo = "fedora-43-everything" },
    { set  = "azl4-prod" },
]
# Image build: same, plus the Koji dist-repo (only valid here because its
# gpg-key is a local path, which mock can't resolve inside the chroot).
image-build = [
    { repo = "fedora-43-everything" },
    { set  = "azl4-prod" },
    { set  = "koji-build-deps" },
]
```

Inspect the fully resolved configuration with:

```sh
azldev config dump -q -O json | jq '.resources, .distros.mydistro.versions["4.0"].inputs'
```

## Authoring tips

- **Singular, one-off repos belong in `[resources.rpm-repos.*]`.** They don't need a template — reference them in inputs as `{ repo = "..." }`.
- **Prefer repo sets over individual `rpm-repos` for layouts you'll deploy more than once.** Two mirrors of the same layout become two `rpm-repo-sets` entries that share a template instead of 12 hand-maintained `rpm-repos` entries that drift.
- **Put the `gpg-key` on the *set* (not on each synthesized repo).** Sub-repos in a published tree always share a key.
- **Use `name-prefix` to namespace deployments.** `azl4-`, `azl4-staging-`, `koji-build-deps-` make repo IDs self-describing in dnf logs and mock output.
- **`subrepos = ["base", "base-src", "sdk", "sdk-src"]`** is a frequent allowlist for build inputs — dnf doesn't need debuginfo to resolve build dependencies, and skipping it makes refresh faster.
- **Define new templates when your layout doesn't fit `azl-standard` or `koji-dist-repo`.** Templates are cheap; don't bend an existing one.

## Related Resources

- [Resources](../reference/config/resources.md) — field-level reference
- [Distros — Inputs](../reference/config/distros.md#inputs) — wiring repos into distro versions
- [Configuration System](config-system.md) — load order, merge rules
