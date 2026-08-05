package manage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/deb-s3/deb-s3/internal/apt"
	"github.com/deb-s3/deb-s3/internal/storage"
)

var ErrNoPackagesFound = errors.New("no packages found in repository")
var ErrNoPackagesDeleted = errors.New("no packages were deleted")

type Progress struct {
	Log      func(string)
	Warn     func(string)
	Detail   func(string)
	Transfer func(string)
}

type Repository struct {
	Store    storage.Store
	Progress Progress
}

type CopyOptions struct {
	SourceCodename       string
	SourceComponent      string
	DestinationCodename  string
	DestinationComponent string
	Architecture         string
	Package              string
	Versions             []string
	VersionsSet          bool
	PreserveVersions     bool
	FailIfExists         bool
	CacheControl         string
	ByHash               *bool
	Signer               apt.ReleaseSigner
}

type DeleteOptions struct {
	Codename     string
	Component    string
	Architecture string
	Package      string
	Versions     []string
	VersionsSet  bool
	Origin       *string
	Suite        *string
	CacheControl string
	ByHash       *bool
	Signer       apt.ReleaseSigner
}

func (r Repository) Copy(ctx context.Context, options CopyOptions) error {
	if r.Store == nil {
		return errors.New("cannot copy without an object store")
	}
	if options.VersionsSet {
		r.log("Versions to copy: " + strings.Join(options.Versions, ", "))
	} else {
		r.warn(fmt.Sprintf("===> WARNING: Copying all versions of %s", options.Package))
	}
	r.log("Retrieving existing manifests")
	source, err := apt.RetrieveManifest(ctx, r.Store, apt.ManifestOptions{
		Codename: options.SourceCodename, Component: options.SourceComponent, Architecture: options.Architecture,
	})
	if err != nil {
		return fmt.Errorf("retrieve source manifest: %w", err)
	}
	release, err := apt.RetrieveRelease(ctx, r.Store, apt.ReleaseOptions{
		Codename: options.DestinationCodename, CacheControl: options.CacheControl, ByHash: options.ByHash, Signer: options.Signer,
	})
	if err != nil {
		return fmt.Errorf("retrieve destination Release: %w", err)
	}
	destination, err := apt.RetrieveManifest(ctx, r.Store, apt.ManifestOptions{
		Codename: options.DestinationCodename, Component: options.DestinationComponent,
		Architecture: options.Architecture, CacheControl: options.CacheControl,
		ByHash: release.ByHash, FailIfExists: options.FailIfExists, SkipPackageUpload: true,
	})
	if err != nil {
		return fmt.Errorf("retrieve destination manifest: %w", err)
	}

	selected := make([]*apt.Package, 0)
	for _, pack := range source.Packages {
		if pack.Name != options.Package {
			continue
		}
		if options.VersionsSet && !slices.Contains(options.Versions, pack.FullVersion()) {
			continue
		}
		selected = append(selected, pack)
	}
	if len(selected) == 0 {
		return ErrNoPackagesFound
	}
	for _, pack := range selected {
		if err := destination.Add(pack, options.PreserveVersions, false); err != nil {
			return fmt.Errorf("prepare destination manifest: %w", err)
		}
	}
	if err := r.publishMetadata(ctx, release, []*apt.Manifest{destination}); err != nil {
		return fmt.Errorf("copy metadata: %w", err)
	}
	r.log("Copy complete.")
	return nil
}

func (r Repository) Delete(ctx context.Context, options DeleteOptions) error {
	if r.Store == nil {
		return errors.New("cannot delete without an object store")
	}
	if options.VersionsSet {
		r.log("Versions to delete: " + strings.Join(options.Versions, ", "))
	} else {
		r.warn(fmt.Sprintf("===> WARNING: Deleting all versions of %s", options.Package))
	}
	r.log("Retrieving existing manifests")
	release, err := apt.RetrieveRelease(ctx, r.Store, apt.ReleaseOptions{
		Codename: options.Codename, Origin: options.Origin, Suite: options.Suite,
		CacheControl: options.CacheControl, ByHash: options.ByHash, Signer: options.Signer,
	})
	if err != nil {
		return fmt.Errorf("retrieve Release for delete: %w", err)
	}
	architectures := []string{options.Architecture}
	if options.Architecture == "all" {
		architectures = append([]string(nil), release.Architectures...)
	}
	changed := make([]*apt.Manifest, 0, len(architectures))
	totalDeleted := 0
	for _, architecture := range architectures {
		manifest, err := apt.RetrieveManifest(ctx, r.Store, apt.ManifestOptions{
			Codename: options.Codename, Component: options.Component, Architecture: architecture,
			CacheControl: options.CacheControl, ByHash: release.ByHash, SkipPackageUpload: true,
		})
		if err != nil {
			return fmt.Errorf("retrieve %s manifest for delete: %w", architecture, err)
		}
		var versions []string
		if options.VersionsSet {
			versions = append([]string{}, options.Versions...)
		}
		deleted := manifest.DeletePackage(options.Package, versions)
		if len(deleted) == 0 {
			if options.VersionsSet {
				r.detail(fmt.Sprintf("No packages were deleted. %s versions %s could not be found in arch %s.", options.Package, strings.Join(options.Versions, ", "), architecture))
			} else {
				r.detail(fmt.Sprintf("No packages were deleted. %s not found in arch %s.", options.Package, architecture))
			}
			continue
		}
		for _, pack := range deleted {
			r.detail(fmt.Sprintf("Deleting %s version %s from arch %s", pack.Name, pack.FullVersion(), architecture))
		}
		totalDeleted += len(deleted)
		changed = append(changed, manifest)
	}
	if totalDeleted == 0 {
		return ErrNoPackagesDeleted
	}
	r.log("Uploading new manifests to S3")
	if err := r.publishMetadata(ctx, release, changed); err != nil {
		return fmt.Errorf("delete metadata: %w", err)
	}
	r.log("Update complete.")
	return nil
}

func (r Repository) publishMetadata(ctx context.Context, release *apt.Release, manifests []*apt.Manifest) error {
	return apt.PublishMetadata(ctx, release, manifests, r.Progress.Transfer)
}

func (r Repository) log(message string) {
	if r.Progress.Log != nil {
		r.Progress.Log(message)
	}
}

func (r Repository) warn(message string) {
	if r.Progress.Warn != nil {
		r.Progress.Warn(message)
	}
}

func (r Repository) detail(message string) {
	if r.Progress.Detail != nil {
		r.Progress.Detail(message)
	}
}
