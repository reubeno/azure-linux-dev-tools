// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/downloader"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
)

func (r *Resolver) fetch(uri, digest, format, arch string) (Artifact, error) {
	if strings.HasPrefix(uri, "docker://") {
		return r.pullContainer(strings.TrimPrefix(uri, "docker://"), arch)
	}

	location, err := url.Parse(uri)
	if err != nil || (location.Scheme != "https" && location.Scheme != "http") || location.Host == "" {
		return Artifact{}, fmt.Errorf("invalid artifact download URL %#q", uri)
	}

	digest = strings.ToLower(digest)
	if digest != "" {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size {
			return Artifact{}, fmt.Errorf("invalid expected SHA-256 digest %#q", digest)
		}
	}

	if r.Env.DryRun() {
		slog.Info("Would resolve published artifact", "uri", uri, "format", format)

		return Artifact{Source: SourceLKG, URI: uri, SHA256: digest, Format: format, Arch: arch}, nil
	}

	path, actual, err := r.materializeDownload(uri, digest, format)
	if err != nil {
		return Artifact{}, err
	}

	resolved, err := r.describe(path, format, arch, SourceLKG, time.Time{})
	resolved.URI, resolved.SHA256 = uri, actual
	slog.Info("Resolved LKG artifact", "uri", uri, "sha256", actual, "build-age", "unknown")

	return resolved, err
}

func (r *Resolver) contentPath(digest, format string) string {
	suffix := format
	if format == "oci" {
		suffix = "oci.tar"
	}

	return filepath.Join(r.Env.WorkDir(), "artifacts", "sha256", digest, digest+"."+suffix)
}

func (r *Resolver) materializeDownload(uri, expected, format string) (path, digest string, err error) {
	if expected != "" {
		path := r.contentPath(expected, format)

		actual, err := hashFile(r.Env.FS(), path)
		if err == nil {
			if actual != expected {
				return "", "", fmt.Errorf("cached artifact SHA-256 mismatch at %#q: expected %s, got %s", path, expected, actual)
			}

			return path, expected, nil
		}

		if !errors.Is(err, fs.ErrNotExist) {
			return "", "", err
		}
	}

	root := filepath.Join(r.Env.WorkDir(), "artifacts")
	if err := fileutils.MkdirAll(r.Env.FS(), root); err != nil {
		return "", "", fmt.Errorf("create artifact cache:\n%w", err)
	}

	stage, err := fileutils.MkdirTemp(r.Env.FS(), root, ".download-*")
	if err != nil {
		return "", "", fmt.Errorf("create private artifact download:\n%w", err)
	}

	defer func() {
		if cleanupErr := r.Env.FS().RemoveAll(stage); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove temporary artifact download:\n%w", cleanupErr))
		}
	}()

	temporary := filepath.Join(stage, "download")
	if err := r.downloadInto(uri, temporary); err != nil {
		return "", "", err
	}

	actual, err := hashFile(r.Env.FS(), temporary)
	if err != nil {
		return "", "", err
	}

	if expected != "" && actual != expected {
		return "", "", fmt.Errorf("SHA-256 mismatch for %#q: expected %s, got %s", uri, expected, actual)
	}

	dest := r.contentPath(actual, format)
	if err := fileutils.MkdirAll(r.Env.FS(), filepath.Dir(dest)); err != nil {
		return "", "", fmt.Errorf("create content-addressed artifact directory:\n%w", err)
	}
	// Concurrent publishers of this digest have verified identical bytes. Rename
	// atomically publishes a complete file without changing any other identity.
	if err := r.Env.FS().Rename(temporary, dest); err != nil {
		return "", "", fmt.Errorf("publish downloaded artifact:\n%w", err)
	}

	return dest, actual, nil
}

func (r *Resolver) downloadInto(uri, path string) error {
	client := r.Downloader
	if client == nil {
		var err error

		client, err = downloader.NewHTTPDownloader(r.Env, r.Env, r.Env.FS())
		if err != nil {
			return fmt.Errorf("create artifact downloader:\n%w", err)
		}
	}

	if err := client.Download(r.Env, uri, path); err != nil {
		return fmt.Errorf("download artifact %#q:\n%w", uri, err)
	}

	return nil
}

func hashFile(files opctx.FS, path string) (string, error) {
	file, err := files.Open(path)
	if err != nil {
		return "", fmt.Errorf("open artifact for hashing:\n%w", err)
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash artifact:\n%w", err)
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}
