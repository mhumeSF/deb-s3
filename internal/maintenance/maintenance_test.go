package maintenance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mhumesf/deb-s3-go/internal/apt"
	"github.com/mhumesf/deb-s3-go/internal/storage"
)

func TestVerifyReadOnlyReportsMissingPackages(t *testing.T) {
	ctx := context.Background()
	store := &recordingStore{Store: verificationRepository(t)}
	var missing []string
	result, err := (Repository{Store: store, Progress: Progress{Missing: func(architecture string, pack *apt.Package) {
		missing = append(missing, architecture+":"+pack.Name+"@"+pack.FullVersion())
	}}}).Verify(ctx, VerifyOptions{Codename: "stable", Component: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if result != (VerifyResult{Checked: 3, Missing: 1}) {
		t.Fatalf("Verify() = %#v", result)
	}
	if !reflect.DeepEqual(missing, []string{"amd64:missing@2.0-1"}) {
		t.Fatalf("missing callbacks = %#v", missing)
	}
	if store.puts != 0 || store.deletes != 0 || store.copies != 0 {
		t.Fatalf("read-only verify mutated storage: puts=%d deletes=%d copies=%d", store.puts, store.deletes, store.copies)
	}
}

func TestVerifyRepairRemovesOnlyMissingRecords(t *testing.T) {
	ctx := context.Background()
	base := verificationRepository(t)
	arm64Before, _ := base.Get(ctx, "dists/stable/main/binary-arm64/Packages")
	store := &recordingStore{Store: base}
	result, err := (Repository{Store: store}).Verify(ctx, VerifyOptions{
		Codename: "stable", Component: "main", FixManifests: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != (VerifyResult{Checked: 3, Missing: 1, Repaired: 1}) {
		t.Fatalf("Verify() = %#v", result)
	}
	amd64, err := apt.RetrieveManifest(ctx, base, apt.ManifestOptions{Codename: "stable", Component: "main", Architecture: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if len(amd64.Packages) != 1 || amd64.Packages[0].Name != "present" {
		t.Fatalf("repaired amd64 Packages = %#v", amd64.Packages)
	}
	arm64After, _ := base.Get(ctx, "dists/stable/main/binary-arm64/Packages")
	if !bytes.Equal(arm64Before, arm64After) {
		t.Fatal("repair rewrote an unaffected manifest")
	}
	if _, err := base.Get(ctx, "pool/stable/missing.deb"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing pool object unexpectedly created: %v", err)
	}
	if store.puts != 3 {
		t.Fatalf("repair Put count = %d, want Packages, Packages.gz, Release", store.puts)
	}
}

func TestVerifyFixWithNoMissingPackagesDoesNotPublish(t *testing.T) {
	ctx := context.Background()
	base := verificationRepository(t)
	if err := base.Put(ctx, "pool/stable/missing.deb", strings.NewReader("now present"), storage.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	store := &recordingStore{Store: base}
	result, err := (Repository{Store: store}).Verify(ctx, VerifyOptions{
		Codename: "stable", Component: "main", FixManifests: true,
	})
	if err != nil || result.Missing != 0 || result.Repaired != 0 {
		t.Fatalf("Verify() = %#v, %v", result, err)
	}
	if store.puts != 0 || store.deletes != 0 {
		t.Fatalf("no-op repair mutated storage: puts=%d deletes=%d", store.puts, store.deletes)
	}
}

func TestCleanDeletesOnlyUnreferencedExactCodenamePoolObjects(t *testing.T) {
	ctx := context.Background()
	store := cleanupRepository(t)
	var details []string
	result, err := (Repository{Store: store, Progress: Progress{Detail: func(message string) {
		details = append(details, message)
	}}}).Clean(ctx, CleanOptions{Codename: "stable"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifests != 4 || result.Referenced != 4 || !reflect.DeepEqual(result.Deleted, []string{"pool/stable/unreferenced.deb"}) {
		t.Fatalf("Clean() = %#v", result)
	}
	if !reflect.DeepEqual(details, []string{"Deleting pool/stable/unreferenced.deb"}) {
		t.Fatalf("details = %#v", details)
	}
	for _, key := range []string{
		"pool/stable/referenced-amd64.deb",
		"pool/stable/referenced-arm64.deb",
		"pool/stable/shared.deb",
		"pool/stable/copied.deb",
		"pool/stable-old/unreferenced.deb",
		"pool/testing/unreferenced.deb",
		"dists/stable/main/binary-amd64/by-hash/SHA256/deadbeef",
		"dists/stable-old/main/binary-amd64/Packages",
	} {
		if _, err := store.Get(ctx, key); err != nil {
			t.Fatalf("Clean removed out-of-scope or referenced object %q: %v", key, err)
		}
	}
	if _, err := store.Get(ctx, "pool/stable/unreferenced.deb"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("unreferenced object still exists: %v", err)
	}
}

func TestCleanSupportsNestedCodenameAndRejectsTraversal(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("prefix")
	manifest := apt.NewManifest(store, apt.ManifestOptions{Codename: "stable/updates", Component: "main", Architecture: "amd64"})
	manifest.Packages = []*apt.Package{maintenancePackage("nested", "1.0", "1", "amd64", "pool/stable/updates/referenced.deb")}
	if err := manifest.Publish(ctx, nil); err != nil {
		t.Fatal(err)
	}
	for key, data := range map[string]string{
		"pool/stable/updates/referenced.deb":   "referenced",
		"pool/stable/updates/unreferenced.deb": "unreferenced",
		"pool/stable/unreferenced.deb":         "parent",
	} {
		if err := store.Put(ctx, key, strings.NewReader(data), storage.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := (Repository{Store: store}).Clean(ctx, CleanOptions{Codename: "stable/updates"})
	if err != nil || !reflect.DeepEqual(result.Deleted, []string{"pool/stable/updates/unreferenced.deb"}) {
		t.Fatalf("nested Clean() = %#v, %v", result, err)
	}
	if _, err := store.Get(ctx, "pool/stable/unreferenced.deb"); err != nil {
		t.Fatalf("nested clean touched parent codename: %v", err)
	}
	for _, codename := range []string{"..", "../stable", "/stable"} {
		if _, err := (Repository{Store: store}).Clean(ctx, CleanOptions{Codename: codename}); err == nil {
			t.Fatalf("unsafe codename %q succeeded", codename)
		}
	}
}

func TestCleanUnsafeManifestReferenceAbortsBeforeDeletion(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("")
	packages := "Package: unsafe\nVersion: 1.0-1\nArchitecture: amd64\nFilename: ../../outside.deb\nDescription: unsafe\n"
	if err := store.Put(ctx, "dists/stable/main/binary-amd64/Packages", strings.NewReader(packages), storage.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "pool/stable/unreferenced.deb", strings.NewReader("data"), storage.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (Repository{Store: store}).Clean(ctx, CleanOptions{Codename: "stable"}); err == nil || !strings.Contains(err.Error(), "unsafe referenced package path") {
		t.Fatalf("Clean() error = %v", err)
	}
	if _, err := store.Get(ctx, "pool/stable/unreferenced.deb"); err != nil {
		t.Fatalf("unsafe-reference clean deleted before validation completed: %v", err)
	}
}

func verificationRepository(t *testing.T) *storage.MemoryStore {
	t.Helper()
	ctx := context.Background()
	store := storage.NewMemoryStore("verification-prefix")
	manifests := []*apt.Manifest{
		apt.NewManifest(store, apt.ManifestOptions{Codename: "stable", Component: "main", Architecture: "amd64"}),
		apt.NewManifest(store, apt.ManifestOptions{Codename: "stable", Component: "main", Architecture: "arm64"}),
	}
	manifests[0].Packages = []*apt.Package{
		maintenancePackage("present", "1.0", "1", "amd64", "pool/stable/present.deb"),
		maintenancePackage("missing", "2.0", "1", "amd64", "pool/stable/missing.deb"),
	}
	manifests[1].Packages = []*apt.Package{
		maintenancePackage("arm-present", "1.0", "1", "arm64", "pool/stable/arm-present.deb"),
	}
	release := apt.NewRelease(store, apt.ReleaseOptions{Codename: "stable", Now: maintenanceClock})
	for _, manifest := range manifests {
		if err := manifest.Publish(ctx, nil); err != nil {
			t.Fatal(err)
		}
		release.UpdateManifest(manifest)
	}
	if err := release.Publish(ctx, nil); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"pool/stable/present.deb", "pool/stable/arm-present.deb"} {
		if err := store.Put(ctx, key, strings.NewReader(key), storage.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func cleanupRepository(t *testing.T) *storage.MemoryStore {
	t.Helper()
	ctx := context.Background()
	store := storage.NewMemoryStore("repository-prefix")
	amd64 := apt.NewManifest(store, apt.ManifestOptions{Codename: "stable", Component: "main", Architecture: "amd64"})
	amd64.Packages = []*apt.Package{
		maintenancePackage("amd64", "1.0", "1", "amd64", "pool/stable/referenced-amd64.deb"),
		maintenancePackage("shared", "1.0", "1", "all", "pool/stable/shared.deb"),
	}
	arm64 := apt.NewManifest(store, apt.ManifestOptions{Codename: "stable", Component: "contrib", Architecture: "arm64"})
	arm64.Packages = []*apt.Package{
		maintenancePackage("arm64", "1.0", "1", "arm64", "pool/stable/referenced-arm64.deb"),
		maintenancePackage("shared", "1.0", "1", "all", "pool/stable/shared.deb"),
	}
	for _, manifest := range []*apt.Manifest{amd64, arm64} {
		if err := manifest.Publish(ctx, nil); err != nil {
			t.Fatal(err)
		}
	}
	other := apt.NewManifest(store, apt.ManifestOptions{Codename: "stable-old", Component: "main", Architecture: "amd64"})
	if err := other.Publish(ctx, nil); err != nil {
		t.Fatal(err)
	}
	// Copies are metadata-only, so another codename's manifest can reference
	// objects in this codename's pool.
	copied := apt.NewManifest(store, apt.ManifestOptions{Codename: "testing", Component: "main", Architecture: "amd64"})
	copied.Packages = []*apt.Package{
		maintenancePackage("copied", "1.0", "1", "amd64", "pool/stable/copied.deb"),
	}
	if err := copied.Publish(ctx, nil); err != nil {
		t.Fatal(err)
	}
	objects := map[string]string{
		"pool/stable/copied.deb":                                 "referenced only from the testing codename",
		"pool/stable/referenced-amd64.deb":                       "amd64",
		"pool/stable/referenced-arm64.deb":                       "arm64",
		"pool/stable/shared.deb":                                 "shared",
		"pool/stable/unreferenced.deb":                           "garbage",
		"pool/stable-old/unreferenced.deb":                       "other codename",
		"pool/testing/unreferenced.deb":                          "other repository",
		"dists/stable/main/binary-amd64/by-hash/SHA256/deadbeef": "immutable index",
	}
	for key, data := range objects {
		if err := store.Put(ctx, key, strings.NewReader(data), storage.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func maintenancePackage(name, version, iteration, architecture, filename string) *apt.Package {
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

func maintenanceClock() time.Time {
	return time.Date(2026, time.August, 4, 21, 0, 0, 0, time.UTC)
}

type recordingStore struct {
	storage.Store
	puts    int
	deletes int
	copies  int
}

func (s *recordingStore) Put(ctx context.Context, key string, body io.ReadSeeker, options storage.PutOptions) error {
	s.puts++
	return s.Store.Put(ctx, key, body, options)
}

func (s *recordingStore) Delete(ctx context.Context, key string) error {
	s.deletes++
	return s.Store.Delete(ctx, key)
}

func (s *recordingStore) Copy(ctx context.Context, source, destination string, options storage.CopyOptions) error {
	s.copies++
	return s.Store.Copy(ctx, source, destination, options)
}
