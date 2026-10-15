// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package cachekey creates stable identities for cached resources.
package cachekey

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// From returns the stable identity used by existing NUL-free resource keys.
func From(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))

	return hex.EncodeToString(sum[:])
}
