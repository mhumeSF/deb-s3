package query

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/mhumesf/deb-s3-go/internal/apt"
	"github.com/mhumesf/deb-s3-go/internal/storage"
)

func TestRepositoryListPreservesReleaseAndManifestOrder(t *testing.T) {
	ctx := context.Background()
	store := seededRepository(t)
	repository := Repository{Store: store}
	packages, err := repository.List(ctx, ListOptions{Codename: "stable", Component: "main"})
	if err != nil {
		t.Fatal(err)
	}
	// portable appears in both the arm64 and binary-all manifests but is the
	// same package, so it is listed once.
	if got := packageIdentities(packages); !reflect.DeepEqual(got, []string{"alpha@1:2.0-3", "beta@10", "portable@4.0-1"}) {
		t.Fatalf("List() = %#v", got)
	}
	filtered, err := repository.List(ctx, ListOptions{Codename: "stable", Component: "main", Architecture: "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	if got := packageIdentities(filtered); !reflect.DeepEqual(got, []string{"portable@4.0-1"}) {
		t.Fatalf("filtered List() = %#v", got)
	}
	// "all" is an architecture like any other: it selects the binary-all
	// manifest alone, not every manifest the package was propagated into.
	all, err := repository.List(ctx, ListOptions{Codename: "stable", Component: "main", Architecture: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if got := packageIdentities(all); !reflect.DeepEqual(got, []string{"portable@4.0-1"}) {
		t.Fatalf("List(architecture=all) = %#v", got)
	}
}

func TestRepositoryShowMatchesFullVersion(t *testing.T) {
	repository := Repository{Store: seededRepository(t)}
	pack, err := repository.Show(context.Background(), "stable", "main", "amd64", "alpha", "1:2.0-3")
	if err != nil || pack.Name != "alpha" {
		t.Fatalf("Show() = %#v, %v", pack, err)
	}
	for _, version := range []string{"2.0-3", "1:2.0", "1:2.0-4"} {
		if _, err := repository.Show(context.Background(), "stable", "main", "amd64", "alpha", version); !errors.Is(err, ErrPackageNotFound) {
			t.Fatalf("Show(version=%q) error = %v", version, err)
		}
	}
}

func TestRepositoryExistsChecksEveryName(t *testing.T) {
	repository := Repository{Store: seededRepository(t)}
	matches, err := repository.Exists(context.Background(), "stable", "main", "amd64", "10", []string{"beta", "missing", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Match{
		{Name: "beta", Version: "10", Found: true},
		{Name: "missing", Version: "10", Found: false},
		{Name: "beta", Version: "10", Found: true},
	}
	if !reflect.DeepEqual(matches, want) {
		t.Fatalf("Exists() = %#v, want %#v", matches, want)
	}
}

func TestRepositoryRequiresStore(t *testing.T) {
	repository := Repository{}
	if _, err := repository.List(context.Background(), ListOptions{}); err == nil {
		t.Fatal("List without store succeeded")
	}
	if _, err := repository.Show(context.Background(), "", "", "", "", ""); err == nil {
		t.Fatal("Show without store succeeded")
	}
	if _, err := repository.Exists(context.Background(), "", "", "", "", nil); err == nil {
		t.Fatal("Exists without store succeeded")
	}
}

func seededRepository(t *testing.T) *storage.MemoryStore {
	t.Helper()
	ctx := context.Background()
	store := storage.NewMemoryStore("repository-prefix")
	manifests := []*apt.Manifest{
		apt.NewManifest(store, apt.ManifestOptions{Codename: "stable", Component: "main", Architecture: "amd64"}),
		apt.NewManifest(store, apt.ManifestOptions{Codename: "stable", Component: "main", Architecture: "arm64"}),
		apt.NewManifest(store, apt.ManifestOptions{Codename: "stable", Component: "main", Architecture: "all"}),
	}
	manifests[0].Packages = []*apt.Package{
		queryPackage("alpha", "1", "2.0", "3", "amd64", "pool/alpha.deb"),
		queryPackage("beta", "", "10", "", "amd64", "pool/beta.deb"),
	}
	// Architecture-all packages land in the binary-all manifest and are
	// propagated into every concrete architecture's manifest at upload time.
	manifests[1].Packages = []*apt.Package{
		queryPackage("portable", "", "4.0", "1", "all", "pool/portable.deb"),
	}
	manifests[2].Packages = []*apt.Package{
		queryPackage("portable", "", "4.0", "1", "all", "pool/portable.deb"),
	}
	release := apt.NewRelease(store, apt.ReleaseOptions{Codename: "stable", Now: func() time.Time {
		return time.Date(2026, time.August, 4, 18, 0, 0, 0, time.UTC)
	}})
	for _, manifest := range manifests {
		if err := manifest.Publish(ctx, nil); err != nil {
			t.Fatal(err)
		}
		release.UpdateManifest(manifest)
	}
	if err := release.Publish(ctx, nil); err != nil {
		t.Fatal(err)
	}
	return store
}

func queryPackage(name, epoch, version, iteration, architecture, filename string) *apt.Package {
	pack := apt.NewPackage()
	pack.Name = name
	pack.Epoch = epoch
	pack.Version = version
	pack.Iteration = iteration
	pack.IterationPresent = iteration != ""
	pack.Architecture = architecture
	pack.Category = "utils"
	pack.Maintainer = "Example <example@example.test>"
	pack.IndexFilename = &filename
	return pack
}

func packageIdentities(packages []*apt.Package) []string {
	identities := make([]string, 0, len(packages))
	for _, pack := range packages {
		identities = append(identities, pack.Name+"@"+pack.FullVersion())
	}
	return identities
}
