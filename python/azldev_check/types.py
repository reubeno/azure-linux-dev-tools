# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Domain types for azldev image validation."""

from __future__ import annotations

from dataclasses import dataclass, field


@dataclass(frozen=True)
class PackageInfo:
    """Represents a single package entry from a kiwi .packages manifest."""

    name: str
    epoch: str
    version: str
    release: str
    arch: str
    unknown: str
    license: str

    @property
    def nevra(self) -> str:
        """Return the full NEVRA string (name-epoch:version-release.arch)."""
        epoch_part = f"{self.epoch}:" if self.epoch and self.epoch != "(none)" else ""
        return f"{self.name}-{epoch_part}{self.version}-{self.release}.{self.arch}"


@dataclass(frozen=True)
class FileEntry:
    """Represents a file discovered inside a mounted image."""

    path: str
    is_dir: bool = False
    is_symlink: bool = False
    size: int = 0
    mode: int = 0
    owner: str = ""
    group: str = ""


@dataclass
class ImageConfig:
    """Parsed image configuration from the azldev JSON context file."""

    image_name: str = ""
    image_description: str = ""
    definition_type: str = ""
    definition_path: str = ""
    definition_profile: str = ""
    tests: list[str] = field(default_factory=list)

    @classmethod
    def from_dict(cls, data: dict) -> ImageConfig:
        """Create an ImageConfig from a parsed JSON dictionary."""
        definition = data.get("definition", {})
        return cls(
            image_name=data.get("imageName", ""),
            image_description=data.get("imageDescription", ""),
            definition_type=definition.get("type", ""),
            definition_path=definition.get("path", ""),
            definition_profile=definition.get("profile", ""),
            tests=data.get("tests", []),
        )
