// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package repocompare_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/repo/repocompare"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mapFetcher map[string][]byte

func (f mapFetcher) Fetch(_ context.Context, rawURL string, _ bool) ([]byte, error) {
	return f[rawURL], nil
}

func TestLoadRepositories(t *testing.T) {
	t.Parallel()

	const (
		baseURL = "https://example.com/repo"
		href    = "repodata/primary.xml.gz"
	)

	primary := []byte(`<?xml version="1.0"?>
<metadata xmlns:rpm="http://linux.duke.edu/metadata/rpm">
  <package type="rpm">
    <name>bash</name><arch>x86_64</arch>
    <version epoch="0" ver="5.3" rel="1.azl4"/>
    <checksum type="sha256" pkgid="YES">abc123</checksum>
    <size package="42"/><location href="Packages/b/bash.rpm"/>
    <format><rpm:sourcerpm>bash-5.3-1.azl4.src.rpm</rpm:sourcerpm></format>
  </package>
</metadata>`)

	var compressed bytes.Buffer

	writer := gzip.NewWriter(&compressed)
	_, err := writer.Write(primary)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	repomd := []byte(`<?xml version="1.0"?>
<repomd><revision>123</revision><data type="primary">
  <checksum type="sha256">feed</checksum>
  <location href="repodata/primary.xml.gz"/>
</data></repomd>`)

	packages, snapshots, err := repocompare.LoadRepositories(
		t.Context(),
		mapFetcher{
			baseURL + "/repodata/repomd.xml": repomd,
			baseURL + "/" + href:             compressed.Bytes(),
		},
		[]repocompare.Repository{{
			ID:      "test-base-x86_64",
			Subrepo: "base",
			Kind:    projectconfig.SubrepoKindBinary,
			URL:     baseURL,
		}},
	)
	require.NoError(t, err)
	require.Len(t, packages, 1)
	require.Len(t, snapshots, 1)

	assert.Equal(t, "bash-5.3-1.azl4.x86_64", packages[0].NEVRA())
	assert.Equal(t, "bash-5.3-1.azl4.src.rpm", packages[0].SourceRPM)
	assert.Equal(t, "abc123", packages[0].Checksum)
	assert.Equal(t, int64(42), packages[0].Size)
	assert.Equal(t, "123", snapshots[0].Revision)
	assert.Equal(t, 1, snapshots[0].PackageCount)
	assert.NotEmpty(t, snapshots[0].RepomdSHA256)
}
