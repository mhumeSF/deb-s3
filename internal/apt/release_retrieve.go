package apt

import (
	"context"
	"errors"
	"fmt"

	"github.com/mhumesf/deb-s3-go/internal/storage"
)

func RetrieveRelease(ctx context.Context, store storage.Store, options ReleaseOptions) (*Release, error) {
	if store == nil {
		return nil, errors.New("cannot retrieve Release without an object store")
	}
	filename := NewRelease(store, options).Filename()
	data, err := store.Get(ctx, filename)
	var release *Release
	if errors.Is(err, storage.ErrNotFound) {
		release = NewRelease(store, options)
	} else if err != nil {
		return nil, fmt.Errorf("retrieve Release: %w", err)
	} else {
		release, err = ParseRelease(string(data))
		if err != nil {
			return nil, err
		}
		release.store = store
		release.Now = options.Now
		if release.Now == nil {
			release.Now = NewRelease(nil, ReleaseOptions{}).Now
		}
		release.Codename = options.Codename
		if options.Origin != nil {
			release.Origin = cloneStringPointer(options.Origin)
		}
		if options.Suite != nil {
			release.Suite = cloneStringPointer(options.Suite)
		}
		if options.ByHash != nil {
			release.ByHash = *options.ByHash
		}
		release.CacheControl = options.CacheControl
		release.Signer = options.Signer
	}
	return release, nil
}
