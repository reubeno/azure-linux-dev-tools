// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package artifact describes build outputs and the identity of materialized inputs.
package artifact

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

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
