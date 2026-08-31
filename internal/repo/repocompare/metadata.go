// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package repocompare loads RPM repository metadata and compares package inventories.
package repocompare

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/retry"
	"github.com/ulikunitz/xz"
)

// Repository describes one physical rpm-md repository.
type Repository struct {
	ID               string
	Subrepo          string
	Arch             string
	Kind             projectconfig.SubrepoKind
	URL              string
	PublishChannels  []string
	Unrouted         bool
	DisableSSLVerify bool
}

// Package is one package record from primary repository metadata.
type Package struct {
	Name           string
	Epoch          string
	Version        string
	Release        string
	Arch           string
	Kind           projectconfig.SubrepoKind
	ChecksumType   string
	Checksum       string
	Size           int64
	Location       string
	SourceRPM      string
	RepoID         string
	Subrepo        string
	RepositoryArch string
}

// Identity returns the comparison key for a package.
func (p Package) Identity() string {
	return strings.Join([]string{
		p.Name, normalizedEpoch(p.Epoch), p.Version, p.Release, p.Arch, string(p.Kind),
	}, "\x00")
}

// NEVRA returns a human-readable package identity.
func (p Package) NEVRA() string {
	epoch := ""
	if normalizedEpoch(p.Epoch) != "0" {
		epoch = normalizedEpoch(p.Epoch) + ":"
	}

	return fmt.Sprintf("%s-%s%s-%s.%s", p.Name, epoch, p.Version, p.Release, p.Arch)
}

// NEVR returns a human-readable package identity without its architecture.
func (p Package) NEVR() string {
	epoch := ""
	if normalizedEpoch(p.Epoch) != "0" {
		epoch = normalizedEpoch(p.Epoch) + ":"
	}

	return fmt.Sprintf("%s-%s%s-%s", p.Name, epoch, p.Version, p.Release)
}

// EVR returns the epoch-version-release string.
func (p Package) EVR() string {
	return fmt.Sprintf("%s:%s-%s", normalizedEpoch(p.Epoch), p.Version, p.Release)
}

// Snapshot records the immutable metadata selected from a repository's repomd document.
type Snapshot struct {
	RepoID          string `json:"repoId"                    table:"Repo"`
	URL             string `json:"url"                       table:"URL"`
	Revision        string `json:"revision,omitempty"        table:"Revision"`
	RepomdSHA256    string `json:"repomdSha256"              table:"repomd SHA-256"`
	PrimaryHref     string `json:"primaryHref"               table:"Primary metadata"`
	PrimaryChecksum string `json:"primaryChecksum,omitempty" table:"Primary checksum"`
	PackageCount    int    `json:"packageCount"              table:"Packages"`
}

// Fetcher retrieves one URL into invocation-local memory.
type Fetcher interface {
	// Fetch retrieves rawURL without consulting a persistent cache.
	Fetch(ctx context.Context, rawURL string, disableSSLVerify bool) ([]byte, error)
}

// HTTPFetcher retrieves metadata over HTTP with bounded retries.
type HTTPFetcher struct {
	Attempts int
}

// Fetch implements [Fetcher].
func (f *HTTPFetcher) Fetch(ctx context.Context, rawURL string, disableSSLVerify bool) ([]byte, error) {
	const metadataRequestTimeout = 10 * time.Minute

	attempts := f.Attempts
	if attempts < 1 {
		attempts = 1
	}

	var result []byte

	retryConfig := retry.DefaultConfig()
	retryConfig.MaxAttempts = attempts

	err := retry.Do(ctx, retryConfig, func() error {
		defaultTransport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return errors.New("default HTTP transport is not an *http.Transport")
		}

		transport := defaultTransport.Clone()
		if disableSSLVerify {
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit per-repo opt-out
		}

		client := &http.Client{Transport: transport, Timeout: metadataRequestTimeout}

		request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return fmt.Errorf("creating request for %#q:\n%w", rawURL, err)
		}

		request.Header.Set("Accept-Encoding", "identity")

		response, err := client.Do(request)
		if err != nil {
			return fmt.Errorf("fetching %#q:\n%w", rawURL, err)
		}
		defer response.Body.Close()

		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("fetching %#q returned status %#q", rawURL, response.Status)
		}

		result, err = io.ReadAll(response.Body)
		if err != nil {
			return fmt.Errorf("reading %#q:\n%w", rawURL, err)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("fetching repository metadata:\n%w", err)
	}

	return result, nil
}

type repoMD struct {
	Revision string       `xml:"revision"`
	Data     []repoMDData `xml:"data"`
}

type repoMDData struct {
	Type     string      `xml:"type,attr"`
	Checksum xmlChecksum `xml:"checksum"`
	Location xmlLocation `xml:"location"`
}

type xmlChecksum struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type xmlLocation struct {
	Href string `xml:"href,attr"`
}

type primaryPackage struct {
	Name    string `xml:"name"`
	Arch    string `xml:"arch"`
	Version struct {
		Epoch   string `xml:"epoch,attr"`
		Version string `xml:"ver,attr"`
		Release string `xml:"rel,attr"`
	} `xml:"version"`
	Checksum xmlChecksum `xml:"checksum"`
	Size     struct {
		Package int64 `xml:"package,attr"`
	} `xml:"size"`
	Location  xmlLocation `xml:"location"`
	SourceRPM string      `xml:"format>sourcerpm"`
}

type pendingRepo struct {
	repo       Repository
	repomd     repoMD
	repomdHash string
	primary    repoMDData
}

// LoadRepositories first snapshots every repomd document, then downloads and parses
// each referenced primary metadata file. It never uses the ambient dnf cache.
func LoadRepositories(
	ctx context.Context,
	fetcher Fetcher,
	repositories []Repository,
) ([]Package, []Snapshot, error) {
	if fetcher == nil {
		return nil, nil, errors.New("metadata fetcher cannot be nil")
	}

	pending := make([]pendingRepo, 0, len(repositories))
	for _, repository := range repositories {
		repomdURL, err := joinURL(repository.URL, "repodata/repomd.xml")
		if err != nil {
			return nil, nil, fmt.Errorf("building repomd URL for repository %#q:\n%w", repository.ID, err)
		}

		data, err := fetcher.Fetch(ctx, repomdURL, repository.DisableSSLVerify)
		if err != nil {
			return nil, nil, fmt.Errorf("loading repomd for repository %#q:\n%w", repository.ID, err)
		}

		var metadata repoMD
		if err := xml.Unmarshal(data, &metadata); err != nil {
			return nil, nil, fmt.Errorf("parsing repomd for repository %#q:\n%w", repository.ID, err)
		}

		primary, err := findPrimary(metadata.Data)
		if err != nil {
			return nil, nil, fmt.Errorf("repository %#q:\n%w", repository.ID, err)
		}

		digest := sha256.Sum256(data)
		pending = append(pending, pendingRepo{
			repo:       repository,
			repomd:     metadata,
			repomdHash: hex.EncodeToString(digest[:]),
			primary:    primary,
		})
	}

	var packages []Package

	snapshots := make([]Snapshot, 0, len(pending))

	for _, entry := range pending {
		primaryURL, err := joinURL(entry.repo.URL, entry.primary.Location.Href)
		if err != nil {
			return nil, nil, fmt.Errorf("building primary metadata URL for repository %#q:\n%w", entry.repo.ID, err)
		}

		data, err := fetcher.Fetch(ctx, primaryURL, entry.repo.DisableSSLVerify)
		if err != nil {
			return nil, nil, fmt.Errorf("loading primary metadata for repository %#q:\n%w", entry.repo.ID, err)
		}

		repoPackages, err := parsePrimary(data, entry.primary.Location.Href, entry.repo)
		if err != nil {
			return nil, nil, fmt.Errorf("parsing primary metadata for repository %#q:\n%w", entry.repo.ID, err)
		}

		packages = append(packages, repoPackages...)
		snapshots = append(snapshots, Snapshot{
			RepoID:          entry.repo.ID,
			URL:             entry.repo.URL,
			Revision:        entry.repomd.Revision,
			RepomdSHA256:    entry.repomdHash,
			PrimaryHref:     entry.primary.Location.Href,
			PrimaryChecksum: strings.TrimSpace(entry.primary.Checksum.Value),
			PackageCount:    len(repoPackages),
		})
	}

	return packages, snapshots, nil
}

func findPrimary(data []repoMDData) (repoMDData, error) {
	for _, entry := range data {
		if entry.Type == "primary" {
			if entry.Location.Href == "" {
				return repoMDData{}, errors.New("primary metadata has no location")
			}

			return entry, nil
		}
	}

	return repoMDData{}, errors.New("repomd contains no primary metadata")
}

func parsePrimary(data []byte, href string, repository Repository) ([]Package, error) {
	reader, closeReader, err := decompressedReader(bytes.NewReader(data), href)
	if err != nil {
		return nil, err
	}

	if closeReader != nil {
		defer closeReader()
	}

	decoder := xml.NewDecoder(reader)

	var packages []Package

	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("reading primary XML:\n%w", err)
		}

		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "package" {
			continue
		}

		var raw primaryPackage
		if err := decoder.DecodeElement(&raw, &start); err != nil {
			return nil, fmt.Errorf("reading package element:\n%w", err)
		}

		packages = append(packages, Package{
			Name:           raw.Name,
			Epoch:          raw.Version.Epoch,
			Version:        raw.Version.Version,
			Release:        raw.Version.Release,
			Arch:           raw.Arch,
			Kind:           repository.Kind,
			ChecksumType:   raw.Checksum.Type,
			Checksum:       strings.TrimSpace(raw.Checksum.Value),
			Size:           raw.Size.Package,
			Location:       raw.Location.Href,
			SourceRPM:      raw.SourceRPM,
			RepoID:         repository.ID,
			Subrepo:        repository.Subrepo,
			RepositoryArch: repository.Arch,
		})
	}

	return packages, nil
}

func decompressedReader(reader io.Reader, name string) (io.Reader, func(), error) {
	switch {
	case strings.HasSuffix(name, ".gz"):
		gzipReader, err := gzip.NewReader(reader)
		if err != nil {
			return nil, nil, fmt.Errorf("creating gzip reader:\n%w", err)
		}

		return gzipReader, func() { _ = gzipReader.Close() }, nil
	case strings.HasSuffix(name, ".zst"), strings.HasSuffix(name, ".zstd"):
		zstdReader, err := zstd.NewReader(reader)
		if err != nil {
			return nil, nil, fmt.Errorf("creating zstd reader:\n%w", err)
		}

		return zstdReader, zstdReader.Close, nil
	case strings.HasSuffix(name, ".bz2"):
		return bzip2.NewReader(reader), nil, nil
	case strings.HasSuffix(name, ".xz"):
		xzReader, err := xz.NewReader(reader)
		if err != nil {
			return nil, nil, fmt.Errorf("creating xz reader:\n%w", err)
		}

		return xzReader, nil, nil
	default:
		return reader, nil, nil
	}
}

func joinURL(baseURL, relative string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parsing base URL %#q:\n%w", baseURL, err)
	}

	parsed.Path = path.Join(parsed.Path, relative)

	return parsed.String(), nil
}

func normalizedEpoch(epoch string) string {
	if epoch == "" {
		return "0"
	}

	return epoch
}
