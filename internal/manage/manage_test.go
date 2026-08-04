package manage

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/deb-s3/deb-s3/internal/apt"
	"github.com/deb-s3/deb-s3/internal/storage"
)

func TestCopySelectedVersionsIsMetadataOnly(t *testing.T) {
	ctx := context.Background()
	store := copyRepository(t)
	beforePool, err := store.List(ctx, "pool/")
	if err != nil {
		t.Fatal(err)
	}
	var transfers []string
	repository := Repository{Store: store, Progress: Progress{Transfer: func(filename string) {
		transfers = append(transfers, filename)
	}}}
	err = repository.Copy(ctx, CopyOptions{
		SourceCodename: "stable", SourceComponent: "main",
		DestinationCodename: "testing", DestinationComponent: "contrib",
		Architecture: "amd64", Package: "example", Versions: []string{"1.0-1"}, VersionsSet: true,
		PreserveVersions: true, FailIfExists: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := apt.RetrieveManifest(ctx, store, apt.ManifestOptions{Codename: "testing", Component: "contrib", Architecture: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if got := packageVersions(manifest.Packages, "example"); !reflect.DeepEqual(got, []string{"1.0-1"}) {
		t.Fatalf("copied versions = %#v", got)
	}
	if len(manifest.Packages) != 2 || manifest.Packages[0].Name != "destination-only" {
		t.Fatalf("destination packages = %#v", manifest.Packages)
	}
	copiedFilename, _ := manifest.Packages[1].RepositoryFilename("testing")
	if copiedFilename != "pool/stable/example-1.deb" {
		t.Fatalf("copied Filename = %q", copiedFilename)
	}
	afterPool, err := store.List(ctx, "pool/")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(objectKeys(afterPool), objectKeys(beforePool)) {
		t.Fatalf("copy changed pool objects: before=%#v after=%#v", objectKeys(beforePool), objectKeys(afterPool))
	}
	for _, filename := range transfers {
		if strings.HasPrefix(filename, "pool/") {
			t.Fatalf("copy transferred package object %q", filename)
		}
	}
	if !reflect.DeepEqual(transfers, []string{
		"dists/testing/contrib/binary-amd64/Packages",
		"dists/testing/contrib/binary-amd64/Packages.gz",
		"dists/testing/Release",
	}) {
		t.Fatalf("transfers = %#v", transfers)
	}
}

func TestCopyAllVersionsAndReplacementRules(t *testing.T) {
	ctx := context.Background()

	t.Run("preserve versions", func(t *testing.T) {
		store := copyRepository(t)
		repository := Repository{Store: store}
		if err := repository.Copy(ctx, CopyOptions{
			SourceCodename: "stable", SourceComponent: "main", DestinationCodename: "testing", DestinationComponent: "contrib",
			Architecture: "amd64", Package: "example", PreserveVersions: true,
		}); err != nil {
			t.Fatal(err)
		}
		manifest, _ := apt.RetrieveManifest(ctx, store, apt.ManifestOptions{Codename: "testing", Component: "contrib", Architecture: "amd64"})
		if got := packageVersions(manifest.Packages, "example"); !reflect.DeepEqual(got, []string{"1.0-1", "2.0-1"}) {
			t.Fatalf("versions = %#v", got)
		}
	})

	t.Run("replace versions", func(t *testing.T) {
		store := copyRepository(t)
		repository := Repository{Store: store}
		if err := repository.Copy(ctx, CopyOptions{
			SourceCodename: "stable", SourceComponent: "main", DestinationCodename: "testing", DestinationComponent: "contrib",
			Architecture: "amd64", Package: "example", PreserveVersions: false,
		}); err != nil {
			t.Fatal(err)
		}
		manifest, _ := apt.RetrieveManifest(ctx, store, apt.ManifestOptions{Codename: "testing", Component: "contrib", Architecture: "amd64"})
		if got := packageVersions(manifest.Packages, "example"); !reflect.DeepEqual(got, []string{"2.0-1"}) {
			t.Fatalf("versions = %#v", got)
		}
	})
}

func TestCopyNoMatchAndConflictDoNotPublish(t *testing.T) {
	ctx := context.Background()

	t.Run("no match", func(t *testing.T) {
		store := copyRepository(t)
		before, _ := store.Get(ctx, "dists/testing/contrib/binary-amd64/Packages")
		err := (Repository{Store: store}).Copy(ctx, CopyOptions{
			SourceCodename: "stable", SourceComponent: "main", DestinationCodename: "testing", DestinationComponent: "contrib",
			Architecture: "amd64", Package: "missing",
		})
		if !errors.Is(err, ErrNoPackagesFound) {
			t.Fatalf("Copy() error = %v", err)
		}
		after, _ := store.Get(ctx, "dists/testing/contrib/binary-amd64/Packages")
		if !bytes.Equal(before, after) {
			t.Fatal("no-match copy changed destination")
		}
	})

	t.Run("conflict", func(t *testing.T) {
		store := copyRepository(t)
		manifest, _ := apt.RetrieveManifest(ctx, store, apt.ManifestOptions{Codename: "testing", Component: "contrib", Architecture: "amd64"})
		manifest.Packages = append(manifest.Packages, managedPackage("example", "1.0", "1", "amd64", "pool/testing/conflicting.deb"))
		if err := manifest.Publish(ctx, nil); err != nil {
			t.Fatal(err)
		}
		before, _ := store.Get(ctx, "dists/testing/contrib/binary-amd64/Packages")
		err := (Repository{Store: store}).Copy(ctx, CopyOptions{
			SourceCodename: "stable", SourceComponent: "main", DestinationCodename: "testing", DestinationComponent: "contrib",
			Architecture: "amd64", Package: "example", Versions: []string{"1.0-1"}, VersionsSet: true,
			PreserveVersions: true, FailIfExists: true,
		})
		if !errors.Is(err, apt.ErrPackageConflict) {
			t.Fatalf("Copy() error = %v", err)
		}
		after, _ := store.Get(ctx, "dists/testing/contrib/binary-amd64/Packages")
		if !bytes.Equal(before, after) {
			t.Fatal("conflicting copy changed destination")
		}
	})
}

func TestDeleteAcrossArchitecturesLeavesPoolObjects(t *testing.T) {
	ctx := context.Background()
	store := deleteRepository(t)
	beforePool, _ := store.List(ctx, "pool/")
	var details []string
	err := (Repository{Store: store, Progress: Progress{Detail: func(message string) {
		details = append(details, message)
	}}}).Delete(ctx, DeleteOptions{
		Codename: "stable", Component: "main", Architecture: "all", Package: "example",
		Versions: []string{"1.0"}, VersionsSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	amd64, _ := apt.RetrieveManifest(ctx, store, apt.ManifestOptions{Codename: "stable", Component: "main", Architecture: "amd64"})
	arm64, _ := apt.RetrieveManifest(ctx, store, apt.ManifestOptions{Codename: "stable", Component: "main", Architecture: "arm64"})
	if got := packageVersions(amd64.Packages, "example"); !reflect.DeepEqual(got, []string{"2.0-1"}) {
		t.Fatalf("amd64 versions = %#v", got)
	}
	if got := packageVersions(arm64.Packages, "example"); len(got) != 0 {
		t.Fatalf("arm64 versions = %#v", got)
	}
	if len(details) != 2 || !strings.Contains(details[0], "arch amd64") || !strings.Contains(details[1], "arch arm64") {
		t.Fatalf("details = %#v", details)
	}
	afterPool, _ := store.List(ctx, "pool/")
	if !reflect.DeepEqual(objectKeys(afterPool), objectKeys(beforePool)) {
		t.Fatalf("delete changed pool: before=%#v after=%#v", objectKeys(beforePool), objectKeys(afterPool))
	}
}

func TestDeleteNoMatchLeavesMetadataUnchanged(t *testing.T) {
	ctx := context.Background()
	store := deleteRepository(t)
	beforeRelease, _ := store.Get(ctx, "dists/stable/Release")
	beforeAMD64, _ := store.Get(ctx, "dists/stable/main/binary-amd64/Packages")
	var details []string
	err := (Repository{Store: store, Progress: Progress{Detail: func(message string) {
		details = append(details, message)
	}}}).Delete(ctx, DeleteOptions{
		Codename: "stable", Component: "main", Architecture: "all", Package: "missing",
	})
	if !errors.Is(err, ErrNoPackagesDeleted) {
		t.Fatalf("Delete() error = %v", err)
	}
	afterRelease, _ := store.Get(ctx, "dists/stable/Release")
	afterAMD64, _ := store.Get(ctx, "dists/stable/main/binary-amd64/Packages")
	if !bytes.Equal(beforeRelease, afterRelease) || !bytes.Equal(beforeAMD64, afterAMD64) {
		t.Fatal("no-match delete changed metadata")
	}
	if len(details) != 2 {
		t.Fatalf("details = %#v", details)
	}
}

func TestDeleteExplicitEmptyVersionsDeletesNothing(t *testing.T) {
	ctx := context.Background()
	store := deleteRepository(t)
	before, _ := store.Get(ctx, "dists/stable/main/binary-amd64/Packages")
	err := (Repository{Store: store}).Delete(ctx, DeleteOptions{
		Codename: "stable", Component: "main", Architecture: "amd64", Package: "example",
		VersionsSet: true, Versions: nil,
	})
	if !errors.Is(err, ErrNoPackagesDeleted) {
		t.Fatalf("Delete() error = %v", err)
	}
	after, _ := store.Get(ctx, "dists/stable/main/binary-amd64/Packages")
	if !bytes.Equal(before, after) {
		t.Fatal("explicit empty versions deleted packages")
	}
}

func copyRepository(t *testing.T) *storage.MemoryStore {
	t.Helper()
	store := storage.NewMemoryStore("prefix")
	seedRelease(t, store, "stable", map[string][]*apt.Package{
		"main/amd64": {
			managedPackage("example", "1.0", "1", "amd64", "pool/stable/example-1.deb"),
			managedPackage("example", "2.0", "1", "amd64", "pool/stable/example-2.deb"),
			managedPackage("other", "1.0", "1", "amd64", "pool/stable/other.deb"),
		},
	})
	seedRelease(t, store, "testing", map[string][]*apt.Package{
		"contrib/amd64": {managedPackage("destination-only", "1.0", "1", "amd64", "pool/testing/destination.deb")},
	})
	ctx := context.Background()
	for key, value := range map[string]string{
		"pool/stable/example-1.deb": "one",
		"pool/stable/example-2.deb": "two",
		"pool/stable/other.deb":     "other",
	} {
		if err := store.Put(ctx, key, bytes.NewReader([]byte(value)), storage.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func deleteRepository(t *testing.T) *storage.MemoryStore {
	t.Helper()
	store := storage.NewMemoryStore("prefix")
	seedRelease(t, store, "stable", map[string][]*apt.Package{
		"main/amd64": {
			managedPackage("example", "1.0", "1", "amd64", "pool/stable/example-amd64-1.deb"),
			managedPackage("example", "2.0", "1", "amd64", "pool/stable/example-amd64-2.deb"),
		},
		"main/arm64": {
			managedPackage("example", "1.0", "1", "arm64", "pool/stable/example-arm64-1.deb"),
			managedPackage("portable", "1.0", "1", "all", "pool/stable/portable.deb"),
		},
	})
	ctx := context.Background()
	for _, key := range []string{
		"pool/stable/example-amd64-1.deb", "pool/stable/example-amd64-2.deb",
		"pool/stable/example-arm64-1.deb", "pool/stable/portable.deb",
	} {
		if err := store.Put(ctx, key, bytes.NewReader([]byte(key)), storage.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func seedRelease(t *testing.T, store storage.Store, codename string, layouts map[string][]*apt.Package) {
	t.Helper()
	ctx := context.Background()
	release := apt.NewRelease(store, apt.ReleaseOptions{Codename: codename, Now: func() time.Time {
		return time.Date(2026, time.August, 4, 19, 0, 0, 0, time.UTC)
	}})
	for _, layout := range []string{"main/amd64", "main/arm64", "contrib/amd64", "contrib/arm64"} {
		packages, exists := layouts[layout]
		if !exists {
			continue
		}
		parts := strings.Split(layout, "/")
		manifest := apt.NewManifest(store, apt.ManifestOptions{Codename: codename, Component: parts[0], Architecture: parts[1]})
		manifest.Packages = packages
		if err := manifest.Publish(ctx, nil); err != nil {
			t.Fatal(err)
		}
		release.UpdateManifest(manifest)
	}
	if err := release.Publish(ctx, nil); err != nil {
		t.Fatal(err)
	}
}

func managedPackage(name, version, iteration, architecture, filename string) *apt.Package {
	pack := apt.NewPackage()
	pack.Name = name
	pack.Version = version
	pack.Iteration = iteration
	pack.IterationPresent = iteration != ""
	pack.Architecture = architecture
	pack.Category = "utils"
	pack.Maintainer = "Example <example@example.test>"
	pack.IndexFilename = &filename
	return pack
}

func packageVersions(packages []*apt.Package, name string) []string {
	versions := make([]string, 0)
	for _, pack := range packages {
		if pack.Name == name {
			versions = append(versions, pack.FullVersion())
		}
	}
	return versions
}

func objectKeys(objects []storage.ObjectInfo) []string {
	keys := make([]string, 0, len(objects))
	for _, object := range objects {
		keys = append(keys, object.Key)
	}
	return keys
}
