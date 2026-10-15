// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cachekey_test

import (
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/utils/cachekey"
	"github.com/stretchr/testify/assert"
)

func TestSequenceIdentity(t *testing.T) {
	assert.Len(t, cachekey.From("tool", "v1"), 64)
	assert.NotEqual(t, cachekey.From("ab", "c"), cachekey.From("a", "bc"))
	assert.NotEqual(t, cachekey.From("tool", "v1"), cachekey.From("tool", "v2"))
}
