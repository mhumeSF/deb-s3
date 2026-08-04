package query

import (
	"context"
	"errors"
	"fmt"

	"github.com/deb-s3/deb-s3/internal/apt"
	"github.com/deb-s3/deb-s3/internal/storage"
)

var ErrPackageNotFound = errors.New("no such package found")

type Repository struct {
	Store storage.Store
}

type ListOptions struct {
	Codename     string
	Component    string
	Architecture string
}

type Match struct {
	Name    string
	Version string
	Found   bool
}

func (r Repository) List(ctx context.Context, options ListOptions) ([]*apt.Package, error) {
	if r.Store == nil {
		return nil, errors.New("cannot list packages without an object store")
	}
	release, err := apt.RetrieveRelease(ctx, r.Store, apt.ReleaseOptions{Codename: options.Codename})
	if err != nil {
		return nil, fmt.Errorf("retrieve Release for list: %w", err)
	}
	packages := make([]*apt.Package, 0)
	for _, architecture := range release.Architectures {
		if options.Architecture != "" && options.Architecture != "all" && architecture != options.Architecture {
			continue
		}
		manifest, err := r.manifest(ctx, options.Codename, options.Component, architecture)
		if err != nil {
			return nil, fmt.Errorf("retrieve %s Packages for list: %w", architecture, err)
		}
		packages = append(packages, manifest.Packages...)
	}
	return packages, nil
}

func (r Repository) Show(ctx context.Context, codename, component, architecture, name, version string) (*apt.Package, error) {
	if r.Store == nil {
		return nil, errors.New("cannot show a package without an object store")
	}
	manifest, err := r.manifest(ctx, codename, component, architecture)
	if err != nil {
		return nil, fmt.Errorf("retrieve Packages for show: %w", err)
	}
	for _, pack := range manifest.Packages {
		if pack.Name == name && pack.FullVersion() == version {
			return pack, nil
		}
	}
	return nil, ErrPackageNotFound
}

func (r Repository) Exists(ctx context.Context, codename, component, architecture, version string, names []string) ([]Match, error) {
	if r.Store == nil {
		return nil, errors.New("cannot check packages without an object store")
	}
	manifest, err := r.manifest(ctx, codename, component, architecture)
	if err != nil {
		return nil, fmt.Errorf("retrieve Packages for exists: %w", err)
	}
	matches := make([]Match, 0, len(names))
	for _, name := range names {
		found := false
		for _, pack := range manifest.Packages {
			if pack.Name == name && pack.FullVersion() == version {
				found = true
				break
			}
		}
		matches = append(matches, Match{Name: name, Version: version, Found: found})
	}
	return matches, nil
}

func (r Repository) manifest(ctx context.Context, codename, component, architecture string) (*apt.Manifest, error) {
	return apt.RetrieveManifest(ctx, r.Store, apt.ManifestOptions{
		Codename: codename, Component: component, Architecture: architecture,
	})
}
