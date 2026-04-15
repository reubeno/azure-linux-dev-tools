# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Image access abstraction for offline inspection.

Provides a context manager for mounting disk images read-only using guestmount
(libguestfs). Falls back to a stub implementation when guestmount is not
available, allowing tests that only need manifest data to still run.
"""

from __future__ import annotations

import os
import shutil
import subprocess
import tempfile
from contextlib import contextmanager
from pathlib import Path
from typing import Iterator

from .types import FileEntry


def _guestmount_available() -> bool:
    """Check whether guestmount is available on the system."""
    return shutil.which("guestmount") is not None


@contextmanager
def mount_image(image_path: str | Path) -> Iterator[Path]:
    """Mount a disk image read-only and yield the mountpoint path.

    Uses guestmount (libguestfs) to mount the first filesystem found in the
    image. The mountpoint is automatically cleaned up on exit.

    Args:
        image_path: Path to the disk image file.

    Yields:
        Path to the temporary mountpoint directory.

    Raises:
        RuntimeError: If guestmount is not available or mounting fails.
    """
    if not _guestmount_available():
        raise RuntimeError(
            "guestmount is not available; install libguestfs-tools to inspect images"
        )

    mountpoint = tempfile.mkdtemp(prefix="azldev-mount-")

    try:
        subprocess.run(
            [
                "guestmount",
                "--add", str(image_path),
                "--inspector",
                "--ro",
                mountpoint,
            ],
            check=True,
            capture_output=True,
            text=True,
        )

        yield Path(mountpoint)
    finally:
        # Attempt to unmount; guestunmount is the clean way.
        try:
            subprocess.run(
                ["guestunmount", mountpoint],
                check=True,
                capture_output=True,
                text=True,
            )
        except (subprocess.CalledProcessError, FileNotFoundError):
            # Best-effort fallback.
            subprocess.run(
                ["fusermount", "-u", mountpoint],
                check=False,
                capture_output=True,
            )
        finally:
            # Remove the temporary mountpoint directory.
            if os.path.isdir(mountpoint):
                os.rmdir(mountpoint)


def list_files(root: Path, relative: bool = True) -> list[FileEntry]:
    """Walk a directory tree and return a list of FileEntry objects.

    Args:
        root: Root directory to walk (typically a mounted image).
        relative: If True, paths are relative to root.

    Returns:
        List of FileEntry objects for all files and directories.
    """
    entries: list[FileEntry] = []

    for dirpath, dirnames, filenames in os.walk(root):
        base = Path(dirpath)

        for d in sorted(dirnames):
            full = base / d
            rel = full.relative_to(root) if relative else full
            stat = full.lstat()
            entries.append(
                FileEntry(
                    path=f"/{rel}",
                    is_dir=True,
                    is_symlink=full.is_symlink(),
                    size=0,
                    mode=stat.st_mode,
                    owner=str(stat.st_uid),
                    group=str(stat.st_gid),
                )
            )

        for f in sorted(filenames):
            full = base / f
            rel = full.relative_to(root) if relative else full
            stat = full.lstat()
            entries.append(
                FileEntry(
                    path=f"/{rel}",
                    is_dir=False,
                    is_symlink=full.is_symlink(),
                    size=stat.st_size,
                    mode=stat.st_mode,
                    owner=str(stat.st_uid),
                    group=str(stat.st_gid),
                )
            )

    return entries
