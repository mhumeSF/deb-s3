package apt

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/deb-s3/deb-s3/internal/storage"
)

func TestParseAndRenderPackages(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "Packages.golden"))
	if err != nil {
		t.Fatal(err)
	}
	second := strings.Replace(string(fixture), "Package: discourse", "Package: discourse-helper", 1)
	input := string(fixture) + "\n" + second
	packages, err := ParsePackages(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 2 || packages[0].Name != "discourse" || packages[1].Name != "discourse-helper" {
		t.Fatalf("ParsePackages() = %#v", packages)
	}
	manifest := NewManifest(nil, ManifestOptions{Codename: "stable"})
	manifest.Packages = packages
	output, err := manifest.Render()
	if err != nil {
		t.Fatal(err)
	}
	if output != input {
		firstDifference(t, input, output)
	}
}

func TestParsePackagesReportsParagraph(t *testing.T) {
	_, err := ParsePackages("Package: good\nVersion: 1.0\nFilename: good.deb\n\nPackage: bad\n")
	if err == nil || !strings.Contains(err.Error(), "paragraph 2") {
		t.Fatalf("ParsePackages() error = %v", err)
	}
}

func TestManifestAddVersionRules(t *testing.T) {
	tests := []struct {
		name             string
		preserveVersions bool
		wantVersions     []string
	}{
		{name: "replace all versions", preserveVersions: false, wantVersions: []string{"2.0-1"}},
		{name: "replace exact version", preserveVersions: true, wantVersions: []string{"1.0-1", "2.0-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := NewManifest(nil, ManifestOptions{Codename: "stable"})
			manifest.Packages = []*Package{
				testPackage("example", "1.0", "1", "pool/old-1.deb"),
				testPackage("example", "2.0", "1", "pool/old-2.deb"),
				testPackage("other", "1.0", "1", "pool/other.deb"),
			}
			newPackage := testPackage("example", "2.0", "1", "pool/new-2.deb")
			if err := manifest.Add(newPackage, tt.preserveVersions, true); err != nil {
				t.Fatal(err)
			}
			var versions []string
			for _, pack := range manifest.Packages {
				if pack.Name == "example" {
					versions = append(versions, pack.FullVersion())
				}
			}
			if !reflect.DeepEqual(versions, tt.wantVersions) {
				t.Fatalf("example versions = %#v, want %#v", versions, tt.wantVersions)
			}
			if !reflect.DeepEqual(manifest.PendingUploads, []*Package{newPackage}) {
				t.Fatalf("PendingUploads = %#v", manifest.PendingUploads)
			}
		})
	}
}

func TestManifestAddConflictRules(t *testing.T) {
	manifest := NewManifest(nil, ManifestOptions{Codename: "stable", FailIfExists: true})
	manifest.Packages = []*Package{testPackage("example", "1.0", "1", "pool/example-one.deb")}

	different := testPackage("example", "1.0", "1", "pool/example-two.deb")
	if err := manifest.Add(different, true, true); !errors.Is(err, ErrPackageConflict) {
		t.Fatalf("Add() error = %v, want ErrPackageConflict", err)
	}
	if len(manifest.Packages) != 1 || len(manifest.PendingUploads) != 0 {
		t.Fatalf("conflicting Add mutated manifest: packages=%d pending=%d", len(manifest.Packages), len(manifest.PendingUploads))
	}

	sameBasename := testPackage("example", "1.0", "1", "other/example-one.deb")
	if err := manifest.Add(sameBasename, true, false); err != nil {
		t.Fatalf("same-basename Add() = %v", err)
	}
	if len(manifest.PendingUploads) != 0 {
		t.Fatalf("needsUploading=false added pending upload")
	}
}

func TestManifestDeletePackageVersionForms(t *testing.T) {
	manifest := NewManifest(nil, ManifestOptions{})
	manifest.Packages = []*Package{
		testPackageWithEpoch("example", "2", "1.0", "1"),
		testPackageWithEpoch("example", "2", "1.0", "2"),
		testPackageWithEpoch("example", "", "2.0", "1"),
		testPackageWithEpoch("other", "", "1.0", "1"),
	}
	deleted := manifest.DeletePackage("example", []string{"2:1.0-1", "2.0"})
	if len(deleted) != 2 || deleted[0].FullVersion() != "2:1.0-1" || deleted[1].FullVersion() != "2.0-1" {
		t.Fatalf("deleted = %#v", deleted)
	}
	if len(manifest.Packages) != 2 || manifest.Packages[0].FullVersion() != "2:1.0-2" || manifest.Packages[1].Name != "other" {
		t.Fatalf("retained = %#v", manifest.Packages)
	}

	if deleted := manifest.DeletePackage("example", []string{}); len(deleted) != 0 {
		t.Fatalf("empty version selection deleted %#v", deleted)
	}
	if deleted := manifest.DeletePackage("example", nil); len(deleted) != 1 {
		t.Fatalf("nil version selection deleted %#v", deleted)
	}
}

func TestRetrieveManifestExistingAndMissing(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("prefix")
	options := ManifestOptions{Codename: "stable", Component: "main", Architecture: "amd64", CacheControl: "max-age=60", FailIfExists: true}
	missing, err := RetrieveManifest(ctx, store, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing.Packages) != 0 || missing.Codename != "stable" || !missing.FailIfExists {
		t.Fatalf("missing manifest = %#v", missing)
	}

	fixture, err := os.ReadFile(filepath.Join("testdata", "Packages.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "dists/stable/main/binary-amd64/Packages", bytes.NewReader(fixture), storage.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	existing, err := RetrieveManifest(ctx, store, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(existing.Packages) != 1 || existing.Packages[0].Name != "discourse" || len(existing.PendingUploads) != 0 {
		t.Fatalf("existing manifest = %#v", existing)
	}
}

func TestManifestBuildArtifactsIsReproducible(t *testing.T) {
	manifest := NewManifest(nil, ManifestOptions{Codename: "stable", Component: "main", Architecture: "amd64"})
	manifest.Packages = []*Package{testPackage("example", "1.0", "1", "pool/example.deb")}
	first, err := manifest.BuildArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	second, err := manifest.BuildArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Packages.Data, second.Packages.Data) || !bytes.Equal(first.PackagesGzip.Data, second.PackagesGzip.Data) {
		t.Fatal("BuildArtifacts output is not reproducible")
	}
	if first.Packages.Path != "dists/stable/main/binary-amd64/Packages" || first.PackagesGzip.Path != "dists/stable/main/binary-amd64/Packages.gz" {
		t.Fatalf("artifact paths = %q, %q", first.Packages.Path, first.PackagesGzip.Path)
	}
	if first.Packages.Hashes != hashManifest(first.Packages.Data) || first.PackagesGzip.Hashes != hashManifest(first.PackagesGzip.Data) {
		t.Fatal("artifact hashes do not describe artifact bytes")
	}
	if len(first.PackagesGzip.Data) < 10 || !bytes.Equal(first.PackagesGzip.Data[4:8], []byte{0, 0, 0, 0}) {
		t.Fatalf("gzip mtime is not zero: %x", first.PackagesGzip.Data[:10])
	}
	reader, err := gzip.NewReader(bytes.NewReader(first.PackagesGzip.Data))
	if err != nil {
		t.Fatal(err)
	}
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decompressed, first.Packages.Data) {
		t.Fatal("Packages.gz does not contain Packages bytes")
	}
}

func TestManifestPublish(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("repository")
	packageData := []byte("package contents")
	packageFile := filepath.Join(t.TempDir(), "example_1.0-1_amd64.deb")
	if err := os.WriteFile(packageFile, packageData, 0o600); err != nil {
		t.Fatal(err)
	}
	pack := testPackage("example", "1.0", "1", "")
	pack.Filename = packageFile

	manifest := NewManifest(store, ManifestOptions{
		Codename:     "stable",
		Component:    "main",
		Architecture: "amd64",
		CacheControl: "max-age=60",
	})
	if err := manifest.Add(pack, false, true); err != nil {
		t.Fatal(err)
	}
	var transferred []string
	if err := manifest.Publish(ctx, func(name string) { transferred = append(transferred, name) }); err != nil {
		t.Fatal(err)
	}
	wantTransferred := []string{
		"pool/stable/e/ex/example_1.0-1_amd64.deb",
		"dists/stable/main/binary-amd64/Packages",
		"dists/stable/main/binary-amd64/Packages.gz",
	}
	if !reflect.DeepEqual(transferred, wantTransferred) {
		t.Fatalf("transferred = %#v, want %#v", transferred, wantTransferred)
	}
	storedPackage, err := store.Get(ctx, wantTransferred[0])
	if err != nil || !bytes.Equal(storedPackage, packageData) {
		t.Fatalf("stored package = %q, %v", storedPackage, err)
	}
	plain, err := store.Get(ctx, wantTransferred[1])
	if err != nil {
		t.Fatal(err)
	}
	wantPlain, _ := manifest.Render()
	if string(plain) != wantPlain {
		firstDifference(t, wantPlain, string(plain))
	}
	compressed, err := store.Get(ctx, wantTransferred[2])
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Files["main/binary-amd64/Packages"] != hashManifest(plain) || manifest.Files["main/binary-amd64/Packages.gz"] != hashManifest(compressed) {
		t.Fatalf("manifest Files = %#v", manifest.Files)
	}
	for _, key := range wantTransferred {
		info, err := store.Head(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if info.CacheControl != "max-age=60" {
			t.Fatalf("%s Cache-Control = %q", key, info.CacheControl)
		}
	}
	plainInfo, _ := store.Head(ctx, wantTransferred[1])
	gzipInfo, _ := store.Head(ctx, wantTransferred[2])
	if plainInfo.ContentType != "text/plain; charset=utf-8" || gzipInfo.ContentType != "application/x-gzip; charset=binary" {
		t.Fatalf("manifest content types = %q, %q", plainInfo.ContentType, gzipInfo.ContentType)
	}
}

func TestManifestPublishByHashBeforeCanonicalIndexes(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("repository/prefix")
	manifest := NewManifest(store, ManifestOptions{
		Codename:     "stable/updates",
		Component:    "main",
		Architecture: "amd64",
		CacheControl: "max-age=60",
		ByHash:       true,
	})
	manifest.Packages = []*Package{testPackage("example", "1.0", "1", "pool/example.deb")}
	artifacts, err := manifest.BuildArtifacts()
	if err != nil {
		t.Fatal(err)
	}

	var transferred []string
	if err := manifest.Publish(ctx, func(filename string) { transferred = append(transferred, filename) }); err != nil {
		t.Fatal(err)
	}
	wantTransferred := []string{
		artifacts.Packages.ByHashPath(),
		artifacts.PackagesGzip.ByHashPath(),
		artifacts.Packages.Path,
		artifacts.PackagesGzip.Path,
	}
	if !reflect.DeepEqual(transferred, wantTransferred) {
		t.Fatalf("transferred = %#v, want %#v", transferred, wantTransferred)
	}
	for _, artifact := range []ManifestArtifact{artifacts.Packages, artifacts.PackagesGzip} {
		if strings.Contains(artifact.ByHashPath(), "\\") {
			t.Fatalf("by-hash path uses non-repository separators: %q", artifact.ByHashPath())
		}
		wantPath := path.Join(path.Dir(artifact.Path), "by-hash", "SHA256", artifact.Hashes.SHA256)
		if artifact.ByHashPath() != wantPath {
			t.Fatalf("ByHashPath() = %q, want %q", artifact.ByHashPath(), wantPath)
		}
		stored, err := store.Get(ctx, artifact.ByHashPath())
		if err != nil || !bytes.Equal(stored, artifact.Data) {
			t.Fatalf("by-hash object %q = %x, %v", artifact.ByHashPath(), stored, err)
		}
		info, err := store.Head(ctx, artifact.ByHashPath())
		if err != nil || info.ContentType != artifact.ContentType || info.CacheControl != "max-age=60" {
			t.Fatalf("by-hash info %q = %#v, %v", artifact.ByHashPath(), info, err)
		}
	}
	if len(manifest.Files) != 2 {
		t.Fatalf("Release checksum files include by-hash entries: %#v", manifest.Files)
	}
	if _, exists := manifest.Files[artifacts.Packages.ByHashPath()]; exists {
		t.Fatal("by-hash path was added to Release checksum files")
	}
}

func TestManifestByHashConflictStopsBeforeCanonicalPublication(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("")
	manifest := NewManifest(store, ManifestOptions{
		Codename: "stable", Component: "main", Architecture: "amd64", ByHash: true,
	})
	manifest.Packages = []*Package{testPackage("example", "1.0", "1", "pool/example.deb")}
	artifacts, err := manifest.BuildArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, artifacts.Packages.ByHashPath(), bytes.NewReader([]byte("corrupt collision")), storage.PutOptions{}); err != nil {
		t.Fatal(err)
	}

	err = manifest.Publish(ctx, nil)
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("Publish() error = %v, want storage.ErrConflict", err)
	}
	for _, filename := range []string{artifacts.Packages.Path, artifacts.PackagesGzip.Path} {
		if _, err := store.Get(ctx, filename); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("canonical index %q was published after immutable conflict: %v", filename, err)
		}
	}
}

func TestManifestPublishSkipAndConflict(t *testing.T) {
	ctx := context.Background()
	packageFile := filepath.Join(t.TempDir(), "example.deb")
	if err := os.WriteFile(packageFile, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	pack := testPackage("example", "1.0", "1", "")
	pack.Filename = packageFile
	poolPath, _ := pack.RepositoryFilename("stable")

	t.Run("skip package upload", func(t *testing.T) {
		store := storage.NewMemoryStore("")
		manifest := NewManifest(store, ManifestOptions{Codename: "stable", Component: "main", Architecture: "amd64", SkipPackageUpload: true})
		if err := manifest.Add(pack, false, true); err != nil {
			t.Fatal(err)
		}
		if err := manifest.Publish(ctx, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Get(ctx, poolPath); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("skipped package Get() error = %v", err)
		}
	})

	t.Run("conflicting package object", func(t *testing.T) {
		store := storage.NewMemoryStore("")
		if err := store.Put(ctx, poolPath, bytes.NewReader([]byte("old")), storage.PutOptions{}); err != nil {
			t.Fatal(err)
		}
		manifest := NewManifest(store, ManifestOptions{Codename: "stable", Component: "main", Architecture: "amd64", FailIfExists: true})
		if err := manifest.Add(pack, false, true); err != nil {
			t.Fatal(err)
		}
		if err := manifest.Publish(ctx, nil); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("Publish() error = %v, want storage.ErrConflict", err)
		}
	})
}

func TestManifestPublishWithNoPendingPackageStillWritesEmptyIndexes(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("")
	manifest := NewManifest(store, ManifestOptions{Codename: "stable", Component: "main", Architecture: "arm64"})
	pack := testPackage("external", "1.0", "1", "external/external.deb")
	if err := manifest.Add(pack, false, false); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Publish(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "external/external.deb"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("non-pending package was uploaded: %v", err)
	}
	if _, err := store.Get(ctx, "dists/stable/main/binary-arm64/Packages"); err != nil {
		t.Fatalf("Packages was not stored: %v", err)
	}
	if len(manifest.Files) != 2 {
		t.Fatalf("Files = %#v", manifest.Files)
	}

	empty := NewManifest(store, ManifestOptions{Codename: "stable", Component: "empty", Architecture: "arm64"})
	if err := empty.Publish(ctx, nil); err != nil {
		t.Fatal(err)
	}
	plain, err := store.Get(ctx, "dists/stable/empty/binary-arm64/Packages")
	if err != nil || len(plain) != 0 {
		t.Fatalf("empty Packages = %q, %v", plain, err)
	}
}

func testPackage(name, version, iteration, indexFilename string) *Package {
	pack := testPackageWithEpoch(name, "", version, iteration)
	if indexFilename != "" {
		pack.IndexFilename = stringPointer(indexFilename, true)
	}
	return pack
}

func testPackageWithEpoch(name, epoch, version, iteration string) *Package {
	pack := NewPackage()
	pack.Name = name
	pack.Epoch = epoch
	pack.Version = version
	pack.Iteration = iteration
	pack.IterationPresent = iteration != ""
	pack.Architecture = "amd64"
	pack.Maintainer = "Example <example@example.test>"
	pack.Category = "utils"
	pack.Priority = stringPointer("optional", true)
	pack.Description = stringPointer("example package", true)
	return pack
}

func TestManifestPathsUseSlashSemantics(t *testing.T) {
	manifest := NewManifest(nil, ManifestOptions{Codename: "stable/updates", Component: "main", Architecture: "amd64"})
	if got := manifest.packagesPath(); got != "dists/stable/updates/main/binary-amd64/Packages" {
		t.Fatalf("packagesPath() = %q", got)
	}
	if got := manifest.releasePackagesPath(); got != "main/binary-amd64/Packages" {
		t.Fatalf("releasePackagesPath() = %q", got)
	}
}

func ExampleManifest_DeletePackage() {
	manifest := NewManifest(nil, ManifestOptions{})
	manifest.Packages = []*Package{testPackage("example", "1.0", "1", "example.deb")}
	deleted := manifest.DeletePackage("example", nil)
	fmt.Println(len(deleted))
	// Output: 1
}
