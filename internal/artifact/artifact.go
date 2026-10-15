// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package artifact describes build outputs and the identity of materialized inputs.
package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// ParseContainerImageID normalizes a single immutable image ID returned by Podman.
func ParseContainerImageID(output string) (string, error) {
	digest := strings.TrimPrefix(strings.TrimSpace(output), "sha256:")

	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size {
		return "", fmt.Errorf("expected a single SHA-256 container image ID, got %#q", output)
	}

	return "sha256:" + strings.ToLower(digest), nil
}

// ValidateName validates an identity used as one managed directory component.
func ValidateName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.IsAbs(name) ||
		filepath.Clean(name) != name || strings.ContainsAny(name, "/\\\x00") {
		return fmt.Errorf("invalid name %#q: expected a simple name without path separators", name)
	}

	return nil
}

// Artifact records a consumed file or OCI image together with its provenance.
// BuiltAt is never inferred from the time a file was downloaded.
type Artifact struct {
	Path       string    `json:"path"`
	Source     string    `json:"source"`
	URI        string    `json:"uri,omitempty"`
	SHA256     string    `json:"sha256,omitempty"`
	Format     string    `json:"format,omitempty"`
	Arch       string    `json:"arch"`
	BuiltAt    time.Time `json:"builtAt,omitempty"`
	ModifiedAt time.Time `json:"modifiedAt,omitempty"`
}

// BuildRecord identifies the complete output set of a successful build.
type BuildRecord struct {
	Name    string    `json:"name"`
	Kind    string    `json:"kind"`
	Arch    string    `json:"arch"`
	BuiltAt time.Time `json:"builtAt"`
	Paths   []string  `json:"paths"`
}
