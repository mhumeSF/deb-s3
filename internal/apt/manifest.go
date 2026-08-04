package apt

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/deb-s3/deb-s3/internal/storage"
)

var ErrPackageConflict = errors.New("package already exists with different contents")

var paragraphSeparator = regexp.MustCompile(`\r?\n[\t ]*\r?\n`)

type ManifestOptions struct {
	Codename          string
	Component         string
	Architecture      string
	CacheControl      string
	ByHash            bool
	FailIfExists      bool
	SkipPackageUpload bool
}

type FileHashes struct {
	Size   int64
	MD5    string
	SHA1   string
	SHA256 string
}

type Manifest struct {
	store storage.Store

	Codename          string
	Component         string
	Architecture      string
	CacheControl      string
	ByHash            bool
	FailIfExists      bool
	SkipPackageUpload bool

	Packages       []*Package
	PendingUploads []*Package
	Files          map[string]FileHashes
}

type PackageConflictError struct {
	Name             string
	Version          string
	ExistingFilename string
	NewFilename      string
}

func (e *PackageConflictError) Error() string {
	return fmt.Sprintf("package %s_%s already exists with different filename (%s)", e.Name, e.Version, e.ExistingFilename)
}

func (e *PackageConflictError) Unwrap() error { return ErrPackageConflict }

func NewManifest(store storage.Store, options ManifestOptions) *Manifest {
	return &Manifest{
		store:             store,
		Codename:          options.Codename,
		Component:         options.Component,
		Architecture:      options.Architecture,
		CacheControl:      options.CacheControl,
		ByHash:            options.ByHash,
		FailIfExists:      options.FailIfExists,
		SkipPackageUpload: options.SkipPackageUpload,
		Files:             make(map[string]FileHashes),
	}
}

func ParsePackages(input string) ([]*Package, error) {
	if strings.TrimSpace(input) == "" {
		return nil, nil
	}
	paragraphs := paragraphSeparator.Split(input, -1)
	packages := make([]*Package, 0, len(paragraphs))
	for index, paragraph := range paragraphs {
		if strings.TrimSpace(paragraph) == "" {
			continue
		}
		pack, err := ParsePackage(paragraph)
		if err != nil {
			return nil, fmt.Errorf("parse package paragraph %d: %w", index+1, err)
		}
		packages = append(packages, pack)
	}
	return packages, nil
}

func (m *Manifest) Render() (string, error) {
	var output strings.Builder
	for index, pack := range m.Packages {
		record, err := pack.Render(m.Codename)
		if err != nil {
			return "", fmt.Errorf("render package %q: %w", pack.Name, err)
		}
		if index > 0 {
			output.WriteByte('\n')
		}
		output.WriteString(record)
	}
	return output.String(), nil
}

func (m *Manifest) Add(pack *Package, preserveVersions bool, needsUploading bool) error {
	if pack == nil {
		return errors.New("cannot add a nil package")
	}
	if m.FailIfExists {
		newFilename, err := pack.RepositoryFilename(m.Codename)
		if err != nil {
			return err
		}
		for _, existing := range m.Packages {
			if existing.Name != pack.Name || existing.FullVersion() != pack.FullVersion() {
				continue
			}
			existingFilename, err := existing.RepositoryFilename(m.Codename)
			if err != nil {
				return err
			}
			if path.Base(existingFilename) != path.Base(newFilename) {
				return &PackageConflictError{
					Name:             pack.Name,
					Version:          pack.FullVersion(),
					ExistingFilename: existingFilename,
					NewFilename:      newFilename,
				}
			}
		}
	}

	retained := m.Packages[:0]
	for _, existing := range m.Packages {
		replace := existing.Name == pack.Name
		if preserveVersions {
			replace = replace && existing.FullVersion() == pack.FullVersion()
		}
		if !replace {
			retained = append(retained, existing)
		}
	}
	m.Packages = append(retained, pack)
	if needsUploading {
		m.PendingUploads = append(m.PendingUploads, pack)
	}
	return nil
}

func (m *Manifest) DeletePackage(name string, versions []string) []*Package {
	versionSet := make(map[string]struct{}, len(versions))
	for _, version := range versions {
		versionSet[version] = struct{}{}
	}
	retained := m.Packages[:0]
	deleted := make([]*Package, 0)
	for _, pack := range m.Packages {
		remove := pack.Name == name && versions == nil
		if pack.Name == name && versions != nil {
			_, baseMatch := versionSet[pack.Version]
			_, iterationMatch := versionSet[pack.Version+"-"+pack.Iteration]
			_, fullMatch := versionSet[pack.FullVersion()]
			remove = baseMatch || iterationMatch || fullMatch
		}
		if remove {
			deleted = append(deleted, pack)
		} else {
			retained = append(retained, pack)
		}
	}
	m.Packages = retained
	return deleted
}

func (m *Manifest) packagesPath() string {
	return path.Join("dists", m.Codename, m.Component, "binary-"+m.Architecture, "Packages")
}

func (m *Manifest) releasePackagesPath() string {
	return path.Join(m.Component, "binary-"+m.Architecture, "Packages")
}
