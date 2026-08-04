package maintenance

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/deb-s3/deb-s3/internal/apt"
	"github.com/deb-s3/deb-s3/internal/storage"
)

type Progress struct {
	Log      func(string)
	Detail   func(string)
	Missing  func(string, *apt.Package)
	Transfer func(string)
}

type Repository struct {
	Store    storage.Store
	Progress Progress
}

type VerifyOptions struct {
	Codename     string
	Component    string
	FixManifests bool
	Origin       *string
	Suite        *string
	CacheControl string
	ByHash       *bool
	Signer       apt.ReleaseSigner
}

type VerifyResult struct {
	Checked  int
	Missing  int
	Repaired int
}

type CleanOptions struct {
	Codename string
}

type CleanResult struct {
	Manifests  int
	Referenced int
	Deleted    []string
}

func (r Repository) Verify(ctx context.Context, options VerifyOptions) (VerifyResult, error) {
	if r.Store == nil {
		return VerifyResult{}, errors.New("cannot verify without an object store")
	}
	r.log("Retrieving existing manifests")
	release, err := apt.RetrieveRelease(ctx, r.Store, apt.ReleaseOptions{
		Codename: options.Codename, Origin: options.Origin, Suite: options.Suite,
		CacheControl: options.CacheControl, ByHash: options.ByHash, Signer: options.Signer,
	})
	if err != nil {
		return VerifyResult{}, fmt.Errorf("retrieve Release for verify: %w", err)
	}
	result := VerifyResult{}
	changed := make([]*apt.Manifest, 0)
	for _, architecture := range release.Architectures {
		r.log(fmt.Sprintf("Checking for missing packages in: %s/%s %s", options.Codename, options.Component, architecture))
		manifest, err := apt.RetrieveManifest(ctx, r.Store, apt.ManifestOptions{
			Codename: options.Codename, Component: options.Component, Architecture: architecture,
			CacheControl: options.CacheControl, ByHash: release.ByHash, SkipPackageUpload: true,
		})
		if err != nil {
			return result, fmt.Errorf("retrieve %s manifest for verify: %w", architecture, err)
		}
		retained := make([]*apt.Package, 0, len(manifest.Packages))
		missingForManifest := 0
		for _, pack := range manifest.Packages {
			result.Checked++
			filename, err := pack.RepositoryFilename(options.Codename)
			if err != nil {
				return result, fmt.Errorf("resolve %s_%s filename: %w", pack.Name, pack.FullVersion(), err)
			}
			_, err = r.Store.Head(ctx, filename)
			if err == nil {
				retained = append(retained, pack)
				continue
			}
			if !errors.Is(err, storage.ErrNotFound) {
				return result, fmt.Errorf("check package object %q: %w", filename, err)
			}
			result.Missing++
			missingForManifest++
			r.missing(architecture, pack)
			if !options.FixManifests {
				retained = append(retained, pack)
			}
		}
		if options.FixManifests && missingForManifest > 0 {
			r.log(fmt.Sprintf("Removing %d package(s) from the manifest...", missingForManifest))
			manifest.Packages = retained
			result.Repaired += missingForManifest
			changed = append(changed, manifest)
		}
	}
	if len(changed) > 0 {
		if err := apt.PublishMetadata(ctx, release, changed, r.Progress.Transfer); err != nil {
			return result, fmt.Errorf("publish repaired metadata: %w", err)
		}
		r.log("Update complete.")
	} else if options.Signer != nil {
		if err := release.Publish(ctx, r.Progress.Transfer); err != nil {
			return result, fmt.Errorf("publish signed Release: %w", err)
		}
		r.log("Update complete.")
	}
	return result, nil
}

func (r Repository) Clean(ctx context.Context, options CleanOptions) (CleanResult, error) {
	if r.Store == nil {
		return CleanResult{}, errors.New("cannot clean without an object store")
	}
	distsPrefix, err := scopedPrefix("dists", options.Codename)
	if err != nil {
		return CleanResult{}, err
	}
	poolPrefix, err := scopedPrefix("pool", options.Codename)
	if err != nil {
		return CleanResult{}, err
	}
	r.log("Retrieving existing manifests")
	objects, err := r.Store.List(ctx, distsPrefix)
	if err != nil {
		return CleanResult{}, fmt.Errorf("list repository manifests: %w", err)
	}
	type manifestLocation struct {
		component    string
		architecture string
	}
	locations := make([]manifestLocation, 0)
	for _, object := range objects {
		relative := strings.TrimPrefix(object.Key, distsPrefix)
		parts := strings.Split(relative, "/")
		if len(parts) != 3 || parts[2] != "Packages" || !strings.HasPrefix(parts[1], "binary-") {
			continue
		}
		architecture := strings.TrimPrefix(parts[1], "binary-")
		if parts[0] == "" || architecture == "" {
			continue
		}
		locations = append(locations, manifestLocation{component: parts[0], architecture: architecture})
	}
	referenced := make(map[string]struct{})
	for _, location := range locations {
		manifest, err := apt.RetrieveManifest(ctx, r.Store, apt.ManifestOptions{
			Codename: options.Codename, Component: location.component, Architecture: location.architecture,
		})
		if err != nil {
			return CleanResult{}, fmt.Errorf("retrieve %s/%s manifest for clean: %w", location.component, location.architecture, err)
		}
		for _, pack := range manifest.Packages {
			filename, err := pack.RepositoryFilename(options.Codename)
			if err != nil {
				return CleanResult{}, fmt.Errorf("resolve referenced package %s_%s: %w", pack.Name, pack.FullVersion(), err)
			}
			normalized, err := storage.JoinKey("", filename)
			if err != nil {
				return CleanResult{}, fmt.Errorf("unsafe referenced package path %q: %w", filename, err)
			}
			referenced[normalized] = struct{}{}
		}
	}
	r.log("Searching for unreferenced packages")
	poolObjects, err := r.Store.List(ctx, poolPrefix)
	if err != nil {
		return CleanResult{}, fmt.Errorf("list package pool: %w", err)
	}
	result := CleanResult{Manifests: len(locations), Referenced: len(referenced)}
	for _, object := range poolObjects {
		if _, exists := referenced[object.Key]; exists {
			continue
		}
		r.detail("Deleting " + object.Key)
		if err := r.Store.Delete(ctx, object.Key); err != nil {
			return result, fmt.Errorf("delete unreferenced package %q: %w", object.Key, err)
		}
		result.Deleted = append(result.Deleted, object.Key)
	}
	return result, nil
}

func scopedPrefix(root, value string) (string, error) {
	cleaned := path.Clean(value)
	if cleaned == "." || path.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("unsafe repository codename %q", value)
	}
	return path.Join(root, cleaned) + "/", nil
}

func (r Repository) log(message string) {
	if r.Progress.Log != nil {
		r.Progress.Log(message)
	}
}

func (r Repository) detail(message string) {
	if r.Progress.Detail != nil {
		r.Progress.Detail(message)
	}
}

func (r Repository) missing(architecture string, pack *apt.Package) {
	if r.Progress.Missing != nil {
		r.Progress.Missing(architecture, pack)
	}
}
