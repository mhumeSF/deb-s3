package apt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"slices"

	"github.com/mhumesf/deb-s3/internal/storage"
)

func (r *Release) ValidateOtherManifests(ctx context.Context, onTransfer func(string)) error {
	if r.store == nil {
		return errors.New("cannot validate Release without an object store")
	}
	created, err := r.MissingManifests()
	if err != nil {
		return err
	}
	for _, manifest := range created {
		if err := manifest.Publish(ctx, onTransfer); err != nil {
			return fmt.Errorf("publish empty manifest for %s/%s: %w", manifest.Component, manifest.Architecture, err)
		}
	}
	for _, manifest := range created {
		r.UpdateManifest(manifest)
	}
	return nil
}

func (r *Release) MissingManifests() ([]*Manifest, error) {
	if r.store == nil {
		return nil, errors.New("cannot find missing manifests without an object store")
	}
	created := make([]*Manifest, 0)
	for _, component := range r.Components {
		for _, architecture := range releaseArchitectures {
			packagesPath := component + "/binary-" + architecture + "/Packages"
			if _, exists := r.Files[packagesPath]; exists || !slices.Contains(r.Architectures, architecture) {
				continue
			}
			manifest := NewManifest(r.store, ManifestOptions{
				Codename:     r.Codename,
				Component:    component,
				Architecture: architecture,
				ByHash:       r.ByHash,
			})
			created = append(created, manifest)
		}
	}
	return created, nil
}

func (r *Release) Publish(ctx context.Context, onTransfer func(string)) error {
	if r.store == nil {
		return errors.New("cannot publish Release without an object store")
	}
	if err := r.ValidateOtherManifests(ctx, onTransfer); err != nil {
		return err
	}
	data, err := r.Render()
	if err != nil {
		return err
	}
	filename := r.Filename()
	var signatures SignatureArtifacts
	if r.Signer != nil {
		signatures, err = r.Signer.Sign(ctx, []byte(data))
		if err != nil {
			return fmt.Errorf("sign Release: %w", err)
		}
		if len(signatures.InRelease) == 0 || len(signatures.ReleaseGPG) == 0 {
			return errors.New("signer returned an empty signature artifact")
		}
	} else {
		for _, stale := range []string{filename + ".gpg", path.Join(path.Dir(filename), "InRelease")} {
			if err := r.store.Delete(ctx, stale); err != nil {
				return fmt.Errorf("remove stale Release signature %q: %w", stale, err)
			}
		}
	}
	if onTransfer != nil {
		onTransfer(filename)
	}
	if err := r.store.Put(ctx, filename, bytes.NewReader([]byte(data)), storage.PutOptions{
		ContentType:  "text/plain; charset=utf-8",
		CacheControl: r.CacheControl,
	}); err != nil {
		return fmt.Errorf("store Release %q: %w", filename, err)
	}
	if r.Signer == nil {
		return nil
	}
	detachedFilename := filename + ".gpg"
	if onTransfer != nil {
		onTransfer(detachedFilename)
	}
	if err := r.store.Put(ctx, detachedFilename, bytes.NewReader(signatures.ReleaseGPG), storage.PutOptions{
		ContentType: "application/pgp-signature; charset=us-ascii", CacheControl: r.CacheControl,
	}); err != nil {
		return fmt.Errorf("store detached Release signature %q: %w", detachedFilename, err)
	}
	inReleaseFilename := path.Join(path.Dir(filename), "InRelease")
	if onTransfer != nil {
		onTransfer(inReleaseFilename)
	}
	if err := r.store.Put(ctx, inReleaseFilename, bytes.NewReader(signatures.InRelease), storage.PutOptions{
		ContentType: "application/pgp-signature; charset=us-ascii", CacheControl: r.CacheControl,
	}); err != nil {
		return fmt.Errorf("store clear-signed Release %q: %w", inReleaseFilename, err)
	}
	return nil
}
