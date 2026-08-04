package apt

import (
	"context"
	"errors"
)

type preparedMetadataManifest struct {
	manifest  *Manifest
	artifacts ManifestArtifacts
}

// PublishMetadata builds and publishes changed manifests plus any required
// empty manifests, then publishes Release. Immutable by-hash objects always
// precede canonical index names across the complete update.
func PublishMetadata(ctx context.Context, release *Release, manifests []*Manifest, onTransfer func(string)) error {
	if release == nil {
		return errors.New("cannot publish metadata without a Release")
	}
	prepared := make([]preparedMetadataManifest, 0, len(manifests))
	prepare := func(manifest *Manifest) error {
		artifacts, err := manifest.BuildArtifacts()
		if err != nil {
			return err
		}
		manifest.RecordArtifacts(artifacts)
		release.UpdateManifest(manifest)
		prepared = append(prepared, preparedMetadataManifest{manifest: manifest, artifacts: artifacts})
		return nil
	}
	for _, manifest := range manifests {
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
	for _, item := range prepared {
		if err := item.manifest.PublishByHash(ctx, item.artifacts, onTransfer); err != nil {
			return err
		}
	}
	for _, item := range prepared {
		if err := item.manifest.PublishCanonical(ctx, item.artifacts, onTransfer); err != nil {
			return err
		}
	}
	return release.Publish(ctx, onTransfer)
}
