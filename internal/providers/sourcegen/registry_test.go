// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sourcegen_test

import (
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/providers/sourcegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubGenerator is a no-op [sourcegen.SourceGenerator] used for registry tests.
type stubGenerator struct{}

var _ sourcegen.SourceGenerator = (*stubGenerator)(nil)

func (s *stubGenerator) CheckPrerequisites(_ opctx.Ctx) error {
	return nil
}

func (s *stubGenerator) Generate(_ opctx.Ctx, _ *sourcegen.GenerateRequest) error {
	return nil
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	reg := sourcegen.NewRegistry()
	gen := &stubGenerator{}

	reg.Register("test-type", gen)

	got, err := reg.Get("test-type")
	require.NoError(t, err)
	assert.Same(t, gen, got)
}

func TestRegistry_Get_Unknown(t *testing.T) {
	reg := sourcegen.NewRegistry()

	_, err := reg.Get("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent")
}

func TestRegistry_Has(t *testing.T) {
	reg := sourcegen.NewRegistry()
	gen := &stubGenerator{}

	assert.False(t, reg.Has("test-type"))

	reg.Register("test-type", gen)

	assert.True(t, reg.Has("test-type"))
	assert.False(t, reg.Has("other"))
}

func TestRegistry_DuplicateRegistration_Panics(t *testing.T) {
	reg := sourcegen.NewRegistry()
	gen := &stubGenerator{}

	reg.Register(projectconfig.OriginTypeURI, gen)

	assert.Panics(t, func() {
		reg.Register(projectconfig.OriginTypeURI, gen)
	})
}
