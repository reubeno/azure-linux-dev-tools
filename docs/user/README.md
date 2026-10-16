# User Guide

## How-To Guides

- [Create a Project](./how-to/create-project.md) — set up a new Azure Linux project
- [Add a Component](./how-to/add-component.md) — import or create a new package
- [Build a Component](./how-to/build-component.md) — build RPMs from component definitions
- [Build an Image](./how-to/build-image.md) — build and boot Azure Linux images
- [Test Build Artifacts](./how-to/test-build-artifacts.md) — list configured tests and run LISA, TMT, or pytest against selected inputs
- [Test a Component](./how-to/test-component.md) — install an existing zlib-ng RPM in a TMT VM and test it
- [Test an Image](./how-to/test-image.md) — complete pytest inspection and LISA guest-test examples
- [Automate Artifact Tests](./how-to/automate-artifact-tests.md) — exit status, JSON results, and CI reports
- [Set Up AI Coding Agents](./how-to/set-up-ai-agents.md) — emit agent skill and instruction files
- [Preview the Lock-File-Free Mode](./how-to/preview-without-lockfile.md) — opt in to `--without-lockfile`

## Explanation

- [Configuration System](./explanation/config-system.md) — how config files are loaded, merged, and how inheritance works
- [RPM Repos & Repo Sets](./explanation/repos.md) — modeling RPM repositories and reusable layout templates
- [TMT Tests & fmf Metadata](./explanation/tmt-tests.md) — how azldev pins and runs TMT plans, and running them locally

## Reference

### Configuration

- [Config File Structure](./reference/config/config-file.md) — root config file layout and includes
- [Project](./reference/config/project.md) — project metadata and directory configuration
- [Distros](./reference/config/distros.md) — distro definitions, versions, and repositories
- [Resources](./reference/config/resources.md) — RPM repos, repo-set templates, and repo sets
- [Components](./reference/config/components.md) — component definitions, spec sources, build options, and source files
- [Overlays](./reference/config/overlays.md) — spec and file overlays for modifying upstream sources
- [Component Groups](./reference/config/component-groups.md) — grouping components with shared defaults
- [Images](./reference/config/images.md) — image definitions (VMs, containers)
- [Tests and Test Groups](./reference/config/tests.md) — framework definitions and shared execution defaults
- [Tools](./reference/config/tools.md) — external tool configuration

### CLI Commands

Auto-generated from `azldev --help`. See [reference/cli/](./reference/cli/azldev.md) for per-command documentation.
