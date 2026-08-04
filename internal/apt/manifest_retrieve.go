package apt

import (
	"context"
	"errors"
	"fmt"

	"github.com/deb-s3/deb-s3/internal/storage"
)

func RetrieveManifest(ctx context.Context, store storage.Store, options ManifestOptions) (*Manifest, error) {
	manifest := NewManifest(store, options)
	if store == nil {
		return nil, errors.New("cannot retrieve manifest without an object store")
	}
	data, err := store.Get(ctx, manifest.packagesPath())
	if errors.Is(err, storage.ErrNotFound) {
		return manifest, nil
	}
	if err != nil {
		return nil, fmt.Errorf("retrieve Packages manifest: %w", err)
	}
	manifest.Packages, err = ParsePackages(string(data))
	if err != nil {
		return nil, err
	}
	return manifest, nil
}
