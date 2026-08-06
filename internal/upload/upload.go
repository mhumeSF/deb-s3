package upload

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/mhumesf/deb-s3/internal/apt"
	"github.com/mhumesf/deb-s3/internal/deb"
	"github.com/mhumesf/deb-s3/internal/storage"
)

var defaultArchitectures = []string{"amd64", "i386", "armhf", "arm64"}

type Options struct {
	Codename          string
	Component         string
	Origin            *string
	Suite             *string
	Architecture      string
	ArchitectureSet   bool
	PreserveVersions  bool
	FailIfExists      bool
	SkipPackageUpload bool
	CacheControl      string
	ByHash            *bool
	Signer            apt.ReleaseSigner
	Now               func() time.Time
}

type Progress struct {
	Log      func(string)
	Warn     func(string)
	Transfer func(string)
}

type Inspector func(context.Context, string) (*apt.Package, error)

type Runner struct {
	Store    storage.Store
	Inspect  Inspector
	Progress Progress
}

type preparedManifest struct {
	manifest  *apt.Manifest
	artifacts apt.ManifestArtifacts
}

func (r Runner) Upload(ctx context.Context, patterns []string, options Options) error {
	if r.Store == nil {
		return errors.New("cannot upload without an object store")
	}
	files, err := ExpandFiles(patterns)
	if err != nil {
		return err
	}
	inspect := r.Inspect
	if inspect == nil {
		inspect = inspectPackage
	}

	r.log("Retrieving existing manifests")
	release, err := apt.RetrieveRelease(ctx, r.Store, apt.ReleaseOptions{
		Codename: options.Codename, Origin: options.Origin, Suite: options.Suite,
		CacheControl: options.CacheControl, ByHash: options.ByHash, Signer: options.Signer, Now: options.Now,
	})
	if err != nil {
		return fmt.Errorf("retrieve repository Release: %w", err)
	}

	manifestOptions := func(architecture string) apt.ManifestOptions {
		return apt.ManifestOptions{
			Codename: options.Codename, Component: options.Component, Architecture: architecture,
			CacheControl: options.CacheControl, ByHash: release.ByHash,
			FailIfExists: options.FailIfExists, SkipPackageUpload: options.SkipPackageUpload,
		}
	}
	manifests := make(map[string]*apt.Manifest)
	ordered := make([]*apt.Manifest, 0, len(release.Architectures)+1)
	manifestFor := func(architecture string) (*apt.Manifest, error) {
		if manifest := manifests[architecture]; manifest != nil {
			return manifest, nil
		}
		manifest, err := apt.RetrieveManifest(ctx, r.Store, manifestOptions(architecture))
		if err != nil {
			return nil, fmt.Errorf("retrieve %s manifest: %w", architecture, err)
		}
		manifests[architecture] = manifest
		ordered = append(ordered, manifest)
		return manifest, nil
	}
	for _, architecture := range release.Architectures {
		if _, err := manifestFor(architecture); err != nil {
			return err
		}
	}

	allPackages := make([]*apt.Package, 0)
	for _, filename := range files {
		r.log("Examining package file " + filepath.Base(filename))
		pack, err := inspect(ctx, filename)
		if err != nil {
			return fmt.Errorf("examine package %q: %w", filename, err)
		}
		architecture := pack.Architecture
		if options.ArchitectureSet {
			architecture = options.Architecture
			if options.Architecture != pack.Architecture {
				r.warn(fmt.Sprintf("You specified architecture %s but package %s has architecture type of %s", options.Architecture, pack.Name, pack.Architecture))
			}
		}
		if architecture == "" {
			return fmt.Errorf("no architecture given and unable to determine one for %s; specify one with --arch", filename)
		}
		if architecture == "all" && len(manifests) == 0 {
			for _, defaultArchitecture := range defaultArchitectures {
				if _, err := manifestFor(defaultArchitecture); err != nil {
					return err
				}
			}
		}
		manifest, err := manifestFor(architecture)
		if err != nil {
			return err
		}
		if err := manifest.Add(pack, options.PreserveVersions, true); err != nil {
			return fmt.Errorf("prepare %s manifest: %w", architecture, err)
		}
		if architecture == "all" {
			allPackages = append(allPackages, pack)
		}
	}
	for architecture, manifest := range manifests {
		if architecture == "all" {
			continue
		}
		for _, pack := range allPackages {
			if err := manifest.Add(pack, options.PreserveVersions, false); err != nil {
				return fmt.Errorf("propagate architecture-all package to %s manifest: %w", architecture, err)
			}
		}
	}

	prepared := make([]preparedManifest, 0, len(ordered))
	prepare := func(manifest *apt.Manifest) error {
		artifacts, err := manifest.BuildArtifacts()
		if err != nil {
			return fmt.Errorf("build %s/%s manifest: %w", manifest.Component, manifest.Architecture, err)
		}
		manifest.RecordArtifacts(artifacts)
		release.UpdateManifest(manifest)
		prepared = append(prepared, preparedManifest{manifest: manifest, artifacts: artifacts})
		return nil
	}
	for _, manifest := range ordered {
		if err := prepare(manifest); err != nil {
			return err
		}
	}
	missing, err := release.MissingManifests()
	if err != nil {
		return err
	}
	for _, manifest := range missing {
		if err := prepare(manifest); err != nil {
			return err
		}
	}

	r.log("Uploading packages and new manifests to S3")
	for _, item := range prepared {
		if err := item.manifest.PublishPackages(ctx, r.Progress.Transfer); err != nil {
			return fmt.Errorf("upload packages for %s/%s: %w", item.manifest.Component, item.manifest.Architecture, err)
		}
	}
	for _, item := range prepared {
		if err := item.manifest.PublishByHash(ctx, item.artifacts, r.Progress.Transfer); err != nil {
			return fmt.Errorf("upload by-hash index for %s/%s: %w", item.manifest.Component, item.manifest.Architecture, err)
		}
	}
	for _, item := range prepared {
		if err := item.manifest.PublishCanonical(ctx, item.artifacts, r.Progress.Transfer); err != nil {
			return fmt.Errorf("upload canonical index for %s/%s: %w", item.manifest.Component, item.manifest.Architecture, err)
		}
	}
	if err := release.Publish(ctx, r.Progress.Transfer); err != nil {
		return fmt.Errorf("publish Release: %w", err)
	}
	r.log("Update complete.")
	return nil
}

func ExpandFiles(patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return nil, errors.New("you must specify at least one file to upload")
	}
	files := make([]string, 0)
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid file pattern %q: %w", pattern, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("file %q doesn't exist", pattern)
		}
		files = append(files, matches...)
	}
	return files, nil
}

func inspectPackage(ctx context.Context, filename string) (*apt.Package, error) {
	info, err := deb.ReadPackageFile(ctx, filename, deb.ReaderOptions{})
	if err != nil {
		return nil, err
	}
	return info.Package, nil
}

func (r Runner) log(message string) {
	if r.Progress.Log != nil {
		r.Progress.Log(message)
	}
}

func (r Runner) warn(message string) {
	if r.Progress.Warn != nil {
		r.Progress.Warn(message)
	}
}
