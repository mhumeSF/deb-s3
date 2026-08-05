package apt

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/deb-s3/deb-s3/internal/storage"
)

func TestReleaseGoldenRoundTrip(t *testing.T) {
	input, err := os.ReadFile(filepath.Join("testdata", "Release.golden"))
	if err != nil {
		t.Fatal(err)
	}
	release, err := ParseRelease(string(input))
	if err != nil {
		t.Fatal(err)
	}
	release.Now = fixedReleaseClock
	if release.Codename != "stable" || release.Origin == nil || *release.Origin != "Example Repository" || release.Suite == nil || *release.Suite != "stable" {
		t.Fatalf("parsed Release = %#v", release)
	}
	if !reflect.DeepEqual(release.Architectures, []string{"amd64", "i386"}) || !reflect.DeepEqual(release.Components, []string{"main", "contrib"}) {
		t.Fatalf("parsed layout: architectures=%#v components=%#v", release.Architectures, release.Components)
	}
	packages := release.Files["main/binary-amd64/Packages"]
	if packages.Size != 12 || len(packages.MD5) != 32 || len(packages.SHA1) != 40 || len(packages.SHA256) != 64 {
		t.Fatalf("parsed package hashes = %#v", packages)
	}
	output, err := release.Render()
	if err != nil {
		t.Fatal(err)
	}
	if output != string(input) {
		firstDifference(t, string(input), output)
	}
}

func TestReleaseRenderOptionalOriginAndSortedFiles(t *testing.T) {
	release := NewRelease(nil, ReleaseOptions{Codename: "stable", Now: fixedReleaseClock})
	release.Architectures = []string{"amd64"}
	release.Components = []string{"main"}
	release.Files["z-last"] = FileHashes{Size: 2, MD5: "z-md5", SHA1: "z-sha1", SHA256: "z-sha256"}
	release.Files["a-first"] = FileHashes{Size: 1, MD5: "a-md5", SHA1: "a-sha1", SHA256: "a-sha256"}
	output, err := release.Render()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "Origin:") {
		t.Fatalf("nil Origin was rendered:\n%s", output)
	}
	if !strings.Contains(output, "Suite: \n") {
		t.Fatalf("nil Suite was not rendered as empty:\n%s", output)
	}
	for _, section := range []string{"MD5Sum:", "SHA1:", "SHA256:"} {
		start := strings.Index(output, section)
		if start < 0 {
			t.Fatalf("missing %s", section)
		}
		sectionOutput := output[start:]
		if strings.Index(sectionOutput, "a-first") > strings.Index(sectionOutput, "z-last") {
			t.Fatalf("files are not sorted in %s:\n%s", section, output)
		}
	}
}

func TestReleaseAcquireByHashParsingRenderingAndOverrides(t *testing.T) {
	enabled := true
	release := NewRelease(nil, ReleaseOptions{Codename: "stable", ByHash: &enabled, Now: fixedReleaseClock})
	output, err := release.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "Acquire-By-Hash: yes\n") {
		t.Fatalf("enabled Release does not advertise by-hash:\n%s", output)
	}
	parsed, err := ParseRelease(output)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.ByHash {
		t.Fatal("ParseRelease did not preserve Acquire-By-Hash: yes")
	}

	ctx := context.Background()
	store := storage.NewMemoryStore("")
	if err := store.Put(ctx, "dists/stable/Release", bytes.NewReader([]byte(output)), storage.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	preserved, err := RetrieveRelease(ctx, store, ReleaseOptions{Codename: "stable", Now: fixedReleaseClock})
	if err != nil || !preserved.ByHash {
		t.Fatalf("unspecified retrieval override = %#v, %v", preserved, err)
	}
	disabled := false
	overridden, err := RetrieveRelease(ctx, store, ReleaseOptions{Codename: "stable", ByHash: &disabled, Now: fixedReleaseClock})
	if err != nil || overridden.ByHash {
		t.Fatalf("explicit disabled override = %#v, %v", overridden, err)
	}
	disabledOutput, err := overridden.Render()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(disabledOutput, "Acquire-By-Hash:") {
		t.Fatalf("disabled Release advertises by-hash:\n%s", disabledOutput)
	}
}

func TestReleaseUpdateManifest(t *testing.T) {
	release := NewRelease(nil, ReleaseOptions{})
	release.Components = []string{"main"}
	release.Architectures = []string{"amd64"}
	manifest := NewManifest(nil, ManifestOptions{Component: "main", Architecture: "amd64"})
	manifest.Files["main/binary-amd64/Packages"] = FileHashes{Size: 10, SHA256: "new"}
	release.UpdateManifest(manifest)
	release.UpdateManifest(manifest)
	if !reflect.DeepEqual(release.Components, []string{"main"}) || !reflect.DeepEqual(release.Architectures, []string{"amd64"}) {
		t.Fatalf("UpdateManifest duplicated layout: %#v %#v", release.Components, release.Architectures)
	}
	if release.Files["main/binary-amd64/Packages"].SHA256 != "new" {
		t.Fatalf("UpdateManifest files = %#v", release.Files)
	}

	second := NewManifest(nil, ManifestOptions{Component: "contrib", Architecture: "arm64"})
	release.UpdateManifest(second)
	if !reflect.DeepEqual(release.Components, []string{"main", "contrib"}) || !reflect.DeepEqual(release.Architectures, []string{"amd64", "arm64"}) {
		t.Fatalf("UpdateManifest append order: %#v %#v", release.Components, release.Architectures)
	}
}

func TestRetrieveReleaseExistingAndMissing(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("repo")
	input, err := os.ReadFile(filepath.Join("testdata", "Release.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "dists/stable/Release", bytes.NewReader(input), storage.PutOptions{}); err != nil {
		t.Fatal(err)
	}

	existing, err := RetrieveRelease(ctx, store, ReleaseOptions{Codename: "stable", Now: fixedReleaseClock})
	if err != nil {
		t.Fatal(err)
	}
	if existing.Origin == nil || *existing.Origin != "Example Repository" || existing.Suite == nil || *existing.Suite != "stable" {
		t.Fatalf("existing fields were not preserved: %#v", existing)
	}
	overrideOrigin := "Override"
	overrideSuite := "testing"
	overridden, err := RetrieveRelease(ctx, store, ReleaseOptions{
		Codename: "stable", Origin: &overrideOrigin, Suite: &overrideSuite,
		CacheControl: "no-cache", Now: fixedReleaseClock,
	})
	if err != nil {
		t.Fatal(err)
	}
	if *overridden.Origin != overrideOrigin || *overridden.Suite != overrideSuite || overridden.CacheControl != "no-cache" {
		t.Fatalf("overridden Release = %#v", overridden)
	}

	missing, err := RetrieveRelease(ctx, store, ReleaseOptions{Codename: "new", Origin: &overrideOrigin, Now: fixedReleaseClock})
	if err != nil {
		t.Fatal(err)
	}
	if missing.Codename != "new" || missing.Origin == nil || *missing.Origin != overrideOrigin || len(missing.Files) != 0 {
		t.Fatalf("missing Release = %#v", missing)
	}
}

func TestValidateOtherManifests(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("repo")
	release := NewRelease(store, ReleaseOptions{Codename: "stable", CacheControl: "release-cache", Now: fixedReleaseClock})
	release.Components = []string{"main", "contrib"}
	release.Architectures = []string{"amd64", "arm64", "riscv64"}
	release.Files["main/binary-amd64/Packages"] = FileHashes{Size: 1}
	release.Files["main/binary-amd64/Packages.gz"] = FileHashes{Size: 2}

	var transferred []string
	if err := release.ValidateOtherManifests(ctx, func(filename string) { transferred = append(transferred, filename) }); err != nil {
		t.Fatal(err)
	}
	wantTransferred := []string{
		"dists/stable/main/binary-arm64/Packages",
		"dists/stable/main/binary-arm64/Packages.gz",
		"dists/stable/contrib/binary-amd64/Packages",
		"dists/stable/contrib/binary-amd64/Packages.gz",
		"dists/stable/contrib/binary-arm64/Packages",
		"dists/stable/contrib/binary-arm64/Packages.gz",
	}
	if !reflect.DeepEqual(transferred, wantTransferred) {
		t.Fatalf("transferred = %#v, want %#v", transferred, wantTransferred)
	}
	if len(release.Files) != 8 {
		t.Fatalf("Release files = %#v", release.Files)
	}
	if _, exists := release.Files["main/binary-riscv64/Packages"]; exists {
		t.Fatal("manifest backfill unexpectedly generated riscv64")
	}
	for _, filename := range wantTransferred {
		info, err := store.Head(ctx, filename)
		if err != nil {
			t.Fatal(err)
		}
		if info.CacheControl != "" {
			t.Fatalf("compatibility empty manifest %s Cache-Control = %q", filename, info.CacheControl)
		}
	}
}

func TestReleaseByHashCoversGeneratedEmptyManifests(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("repo-prefix")
	enabled := true
	release := NewRelease(store, ReleaseOptions{Codename: "stable", ByHash: &enabled, Now: fixedReleaseClock})
	release.Components = []string{"main"}
	release.Architectures = []string{"arm64"}

	var transferred []string
	if err := release.Publish(ctx, func(filename string) { transferred = append(transferred, filename) }); err != nil {
		t.Fatal(err)
	}
	plainHashes := release.Files["main/binary-arm64/Packages"]
	gzipHashes := release.Files["main/binary-arm64/Packages.gz"]
	want := []string{
		path.Join("dists/stable/main/binary-arm64/by-hash/SHA256", plainHashes.SHA256),
		path.Join("dists/stable/main/binary-arm64/by-hash/SHA256", gzipHashes.SHA256),
		"dists/stable/main/binary-arm64/Packages",
		"dists/stable/main/binary-arm64/Packages.gz",
		"dists/stable/Release",
	}
	if !reflect.DeepEqual(transferred, want) {
		t.Fatalf("transferred = %#v, want %#v", transferred, want)
	}
	for _, filename := range want[:2] {
		if _, err := store.Get(ctx, filename); err != nil {
			t.Fatalf("generated empty by-hash object %q: %v", filename, err)
		}
	}
	releaseData, err := store.Get(ctx, "dists/stable/Release")
	if err != nil || !strings.Contains(string(releaseData), "Acquire-By-Hash: yes\n") {
		t.Fatalf("stored Release = %q, %v", releaseData, err)
	}
}

func TestByHashKeepsPreviousReleaseGenerationRetrievable(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("repository-prefix")
	enabled := true
	release := NewRelease(store, ReleaseOptions{Codename: "stable", ByHash: &enabled, Now: fixedReleaseClock})

	publishGeneration := func(version string) (string, ManifestArtifacts) {
		t.Helper()
		manifest := NewManifest(store, ManifestOptions{
			Codename: "stable", Component: "main", Architecture: "amd64", ByHash: true,
		})
		manifest.Packages = []*Package{testPackage("example", version, "1", "pool/example-"+version+".deb")}
		artifacts, err := manifest.BuildArtifacts()
		if err != nil {
			t.Fatal(err)
		}
		if err := manifest.Publish(ctx, nil); err != nil {
			t.Fatal(err)
		}
		release.UpdateManifest(manifest)
		if err := release.Publish(ctx, nil); err != nil {
			t.Fatal(err)
		}
		data, err := store.Get(ctx, release.Filename())
		if err != nil {
			t.Fatal(err)
		}
		return string(data), artifacts
	}

	releaseAData, artifactsA := publishGeneration("1.0")
	releaseA, err := ParseRelease(releaseAData)
	if err != nil {
		t.Fatal(err)
	}
	publishGeneration("2.0")

	for releaseFilename, artifact := range map[string]ManifestArtifact{
		"main/binary-amd64/Packages":    artifactsA.Packages,
		"main/binary-amd64/Packages.gz": artifactsA.PackagesGzip,
	} {
		hashes := releaseA.Files[releaseFilename]
		byHashPath := path.Join("dists", releaseA.Codename, path.Dir(releaseFilename), "by-hash", "SHA256", hashes.SHA256)
		data, err := store.Get(ctx, byHashPath)
		if err != nil || !bytes.Equal(data, artifact.Data) {
			t.Fatalf("generation A object %q after generation B = %x, %v", byHashPath, data, err)
		}
	}
	canonical, err := store.Get(ctx, artifactsA.Packages.Path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(canonical, artifactsA.Packages.Data) {
		t.Fatal("canonical Packages was not replaced by generation B")
	}
}

func TestReleasePublishUnsigned(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("prefix")
	release := NewRelease(store, ReleaseOptions{Codename: "stable", CacheControl: "no-cache", Now: fixedReleaseClock})
	release.Components = []string{"main"}
	release.Architectures = []string{"amd64"}
	release.Files["main/binary-amd64/Packages"] = FileHashes{Size: 1, MD5: "md5", SHA1: "sha1", SHA256: "sha256"}
	release.Files["main/binary-amd64/Packages.gz"] = FileHashes{Size: 2, MD5: "md5gz", SHA1: "sha1gz", SHA256: "sha256gz"}

	if err := store.Put(ctx, "dists/stable/Release.gpg", bytes.NewReader([]byte("stale")), storage.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "dists/stable/InRelease", bytes.NewReader([]byte("existing behavior leaves this")), storage.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	var transferred []string
	if err := release.Publish(ctx, func(filename string) { transferred = append(transferred, filename) }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(transferred, []string{"dists/stable/Release"}) {
		t.Fatalf("transferred = %#v", transferred)
	}
	data, err := store.Get(ctx, "dists/stable/Release")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := release.Render()
	if string(data) != want {
		firstDifference(t, want, string(data))
	}
	info, err := store.Head(ctx, "dists/stable/Release")
	if err != nil || info.ContentType != "text/plain; charset=utf-8" || info.CacheControl != "no-cache" {
		t.Fatalf("Release info = %#v, %v", info, err)
	}
	if _, err := store.Get(ctx, "dists/stable/Release.gpg"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Release.gpg still exists: %v", err)
	}
	if _, err := store.Get(ctx, "dists/stable/InRelease"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("stale InRelease still exists: %v", err)
	}
}

type fakeReleaseSigner struct {
	artifacts SignatureArtifacts
	err       error
	signed    []byte
}

func (s *fakeReleaseSigner) Sign(_ context.Context, release []byte) (SignatureArtifacts, error) {
	s.signed = append([]byte(nil), release...)
	return s.artifacts, s.err
}

func TestReleasePublishSignedArtifactsLast(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("prefix")
	signer := &fakeReleaseSigner{artifacts: SignatureArtifacts{
		InRelease: []byte("clear-signed release"), ReleaseGPG: []byte("detached signature"),
	}}
	release := NewRelease(store, ReleaseOptions{
		Codename: "stable", CacheControl: "no-cache", Signer: signer, Now: fixedReleaseClock,
	})

	var transferred []string
	if err := release.Publish(ctx, func(filename string) { transferred = append(transferred, filename) }); err != nil {
		t.Fatal(err)
	}
	wantTransfers := []string{"dists/stable/Release", "dists/stable/Release.gpg", "dists/stable/InRelease"}
	if !reflect.DeepEqual(transferred, wantTransfers) {
		t.Fatalf("transferred = %#v, want %#v", transferred, wantTransfers)
	}
	rendered, err := release.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(signer.signed, []byte(rendered)) {
		t.Fatal("signer did not receive the rendered Release bytes")
	}
	for filename, want := range map[string][]byte{
		"dists/stable/Release.gpg": signer.artifacts.ReleaseGPG,
		"dists/stable/InRelease":   signer.artifacts.InRelease,
	} {
		data, err := store.Get(ctx, filename)
		if err != nil || !bytes.Equal(data, want) {
			t.Fatalf("%s = %q, %v; want %q", filename, data, err, want)
		}
		info, err := store.Head(ctx, filename)
		if err != nil || info.ContentType != "application/pgp-signature; charset=us-ascii" || info.CacheControl != "no-cache" {
			t.Fatalf("%s info = %#v, %v", filename, info, err)
		}
	}
}

func TestReleaseSigningFailureDoesNotPublishGeneration(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("")
	for filename, data := range map[string]string{
		"dists/stable/Release":     "old Release",
		"dists/stable/Release.gpg": "old detached signature",
		"dists/stable/InRelease":   "old clear signature",
	} {
		if err := store.Put(ctx, filename, bytes.NewReader([]byte(data)), storage.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	signer := &fakeReleaseSigner{err: errors.New("agent refused key")}
	release := NewRelease(store, ReleaseOptions{Codename: "stable", Signer: signer, Now: fixedReleaseClock})
	if err := release.Publish(ctx, nil); err == nil || !strings.Contains(err.Error(), "agent refused key") {
		t.Fatalf("Publish error = %v", err)
	}
	for filename, want := range map[string]string{
		"dists/stable/Release":     "old Release",
		"dists/stable/Release.gpg": "old detached signature",
		"dists/stable/InRelease":   "old clear signature",
	} {
		data, err := store.Get(ctx, filename)
		if err != nil || string(data) != want {
			t.Fatalf("%s changed to %q, %v", filename, data, err)
		}
	}
}

func TestReleaseRejectsEmptySignatureArtifacts(t *testing.T) {
	release := NewRelease(storage.NewMemoryStore(""), ReleaseOptions{
		Codename: "stable", Signer: &fakeReleaseSigner{}, Now: fixedReleaseClock,
	})
	if err := release.Publish(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "empty signature artifact") {
		t.Fatalf("Publish error = %v", err)
	}
}

func TestReleaseRequiresStoreAndClock(t *testing.T) {
	release := NewRelease(nil, ReleaseOptions{Codename: "stable"})
	if err := release.Publish(context.Background(), nil); err == nil {
		t.Fatal("Publish without store succeeded")
	}
	release.Now = nil
	if _, err := release.Render(); err == nil {
		t.Fatal("Render without clock succeeded")
	}
	if _, err := RetrieveRelease(context.Background(), nil, ReleaseOptions{}); err == nil {
		t.Fatal("RetrieveRelease without store succeeded")
	}
}

func fixedReleaseClock() time.Time {
	return time.Date(2026, time.August, 4, 14, 15, 16, 0, time.UTC)
}
