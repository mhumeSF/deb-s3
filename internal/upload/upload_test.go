package upload

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mhumesf/deb-s3/internal/apt"
	"github.com/mhumesf/deb-s3/internal/storage"
)

func TestExpandFiles(t *testing.T) {
	directory := t.TempDir()
	first := writeTestFile(t, directory, "a.deb", "a")
	second := writeTestFile(t, directory, "b.deb", "b")
	files, err := ExpandFiles([]string{filepath.Join(directory, "*.deb"), first})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(files, []string{first, second, first}) {
		t.Fatalf("ExpandFiles() = %#v", files)
	}
	if _, err := ExpandFiles(nil); err == nil {
		t.Fatal("empty patterns succeeded")
	}
	if _, err := ExpandFiles([]string{filepath.Join(directory, "missing*.deb")}); err == nil || !strings.Contains(err.Error(), "doesn't exist") {
		t.Fatalf("missing pattern error = %v", err)
	}
	if _, err := ExpandFiles([]string{"["}); err == nil || !strings.Contains(err.Error(), "invalid file pattern") {
		t.Fatalf("invalid pattern error = %v", err)
	}
}

func TestUploadNewRepositoryPublishesGlobalPhases(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	amd64File := writeTestFile(t, directory, "example-amd64.deb", "amd64 package")
	arm64File := writeTestFile(t, directory, "example-arm64.deb", "arm64 package")
	store := storage.NewMemoryStore("repositories/team")
	enabled := true
	origin := "Example Repository"
	suite := "stable"
	var transfers, logs []string
	runner := Runner{
		Store: store,
		Inspect: inspector(map[string]packageSpec{
			amd64File: {name: "example-amd64", version: "1.0", architecture: "amd64"},
			arm64File: {name: "example-arm64", version: "2.0", architecture: "arm64"},
		}),
		Progress: Progress{
			Log:      func(message string) { logs = append(logs, message) },
			Transfer: func(filename string) { transfers = append(transfers, filename) },
		},
	}
	err := runner.Upload(ctx, []string{filepath.Join(directory, "*.deb")}, Options{
		Codename: "stable", Component: "main", Origin: &origin, Suite: &suite,
		CacheControl: "max-age=60", ByHash: &enabled, Now: fixedClock,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(logs, []string{
		"Retrieving existing manifests",
		"Examining package file example-amd64.deb",
		"Examining package file example-arm64.deb",
		"Uploading packages and new manifests to S3",
		"Update complete.",
	}) {
		t.Fatalf("logs = %#v", logs)
	}
	assertPublicationPhases(t, transfers)
	if len(transfers) != 11 || transfers[len(transfers)-1] != "dists/stable/Release" {
		t.Fatalf("transfers = %#v", transfers)
	}

	releaseData, err := store.Get(ctx, "dists/stable/Release")
	if err != nil {
		t.Fatal(err)
	}
	release, err := apt.ParseRelease(string(releaseData))
	if err != nil {
		t.Fatal(err)
	}
	if !release.ByHash || !reflect.DeepEqual(release.Architectures, []string{"amd64", "arm64"}) || !reflect.DeepEqual(release.Components, []string{"main"}) {
		t.Fatalf("Release layout = %#v", release)
	}
	if release.Origin == nil || *release.Origin != origin || release.Suite == nil || *release.Suite != suite || len(release.Files) != 4 {
		t.Fatalf("Release metadata = %#v", release)
	}
	for architecture, packageName := range map[string]string{"amd64": "example-amd64", "arm64": "example-arm64"} {
		data, err := store.Get(ctx, "dists/stable/main/binary-"+architecture+"/Packages")
		if err != nil || !strings.Contains(string(data), "Package: "+packageName+"\n") {
			t.Fatalf("%s Packages = %q, %v", architecture, data, err)
		}
	}
	for _, filename := range []string{amd64File, arm64File} {
		pack, _ := runner.Inspect(ctx, filename)
		key, _ := pack.RepositoryFilename("stable")
		data, err := store.Get(ctx, key)
		want, _ := os.ReadFile(filename)
		if err != nil || !bytes.Equal(data, want) {
			t.Fatalf("stored package %q = %q, %v", key, data, err)
		}
	}
}

func TestUploadRealDebianPackageThroughNativeReader(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	control := "Package: native-reader\nVersion: 1:2.0-3\nArchitecture: amd64\nMaintainer: Example <example@example.test>\nSection: utils\nPriority: optional\nDescription: Native reader integration\n"
	archive := buildTestDeb(t, control)
	filename := filepath.Join(directory, "native-reader_2.0-3_amd64.deb")
	if err := os.WriteFile(filename, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	store := storage.NewMemoryStore("prefix")
	runner := Runner{Store: store}
	if err := runner.Upload(ctx, []string{filename}, Options{Codename: "stable", Component: "main", Now: fixedClock}); err != nil {
		t.Fatal(err)
	}
	packages, err := store.Get(ctx, "dists/stable/main/binary-amd64/Packages")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"Package: native-reader\n",
		"Version: 1:2.0-3\n",
		"Architecture: amd64\n",
		"Filename: pool/stable/n/na/native-reader_2.0-3_amd64.deb\n",
		fmt.Sprintf("Size: %d\n", len(archive)),
		fmt.Sprintf("SHA256: %x\n", sha256.Sum256(archive)),
	} {
		if !strings.Contains(string(packages), field) {
			t.Fatalf("Packages missing %q:\n%s", field, packages)
		}
	}
	stored, err := store.Get(ctx, "pool/stable/n/na/native-reader_2.0-3_amd64.deb")
	if err != nil || !bytes.Equal(stored, archive) {
		t.Fatalf("stored .deb = %x, %v", stored, err)
	}
}

func TestUploadArchitectureAllInitializesAndPropagates(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	filename := writeTestFile(t, directory, "portable.deb", "portable")
	store := storage.NewMemoryStore("")
	runner := Runner{Store: store, Inspect: inspector(map[string]packageSpec{
		filename: {name: "portable", version: "1.0", architecture: "all"},
	})}
	if err := runner.Upload(ctx, []string{filename}, Options{Codename: "stable", Component: "main", Now: fixedClock}); err != nil {
		t.Fatal(err)
	}
	releaseData, err := store.Get(ctx, "dists/stable/Release")
	if err != nil {
		t.Fatal(err)
	}
	release, err := apt.ParseRelease(string(releaseData))
	if err != nil {
		t.Fatal(err)
	}
	wantArchitectures := []string{"amd64", "i386", "armhf", "arm64", "all"}
	if !reflect.DeepEqual(release.Architectures, wantArchitectures) {
		t.Fatalf("architectures = %#v, want %#v", release.Architectures, wantArchitectures)
	}
	for _, architecture := range wantArchitectures {
		data, err := store.Get(ctx, "dists/stable/main/binary-"+architecture+"/Packages")
		if err != nil || !strings.Contains(string(data), "Package: portable\n") || !strings.Contains(string(data), "Architecture: all\n") {
			t.Fatalf("%s Packages = %q, %v", architecture, data, err)
		}
	}
	objects, err := store.List(ctx, "pool/")
	if err != nil || len(objects) != 1 {
		t.Fatalf("pool objects = %#v, %v", objects, err)
	}
}

func TestUploadArchitectureOverrideMismatchFails(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	filename := writeTestFile(t, directory, "example.deb", "package")
	store := storage.NewMemoryStore("")
	runner := Runner{
		Store: store,
		Inspect: inspector(map[string]packageSpec{
			filename: {name: "example", version: "1.0", architecture: "amd64"},
		}),
	}
	err := runner.Upload(ctx, []string{filename}, Options{
		Codename: "stable", Component: "main", Architecture: "arm64", ArchitectureSet: true, Now: fixedClock,
	})
	if err == nil || !strings.Contains(err.Error(), "Architecture: amd64") || !strings.Contains(err.Error(), "--arch arm64") {
		t.Fatalf("Upload() error = %v", err)
	}
	objects, err := store.List(ctx, "")
	if err != nil || len(objects) != 0 {
		t.Fatalf("mismatch upload published objects: %#v, %v", objects, err)
	}
}

func TestUploadArchitectureOverridePlacesArchAllPackage(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	filename := writeTestFile(t, directory, "portable.deb", "package")
	store := storage.NewMemoryStore("")
	runner := Runner{
		Store: store,
		Inspect: inspector(map[string]packageSpec{
			filename: {name: "portable", version: "1.0", architecture: "all"},
		}),
	}
	if err := runner.Upload(ctx, []string{filename}, Options{
		Codename: "stable", Component: "main", Architecture: "amd64", ArchitectureSet: true, Now: fixedClock,
	}); err != nil {
		t.Fatal(err)
	}
	data, err := store.Get(ctx, "dists/stable/main/binary-amd64/Packages")
	if err != nil || !strings.Contains(string(data), "Package: portable\n") {
		t.Fatalf("amd64 Packages = %q, %v", data, err)
	}
	if _, err := store.Get(ctx, "dists/stable/main/binary-all/Packages"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("arch-all placement fanned out to binary-all: %v", err)
	}
}

func TestUploadArchitectureOverrideSuppliesMissingArchitecture(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	filename := writeTestFile(t, directory, "bare.deb", "package")
	store := storage.NewMemoryStore("")
	runner := Runner{
		Store: store,
		Inspect: inspector(map[string]packageSpec{
			filename: {name: "bare", version: "1.0", architecture: ""},
		}),
	}
	if err := runner.Upload(ctx, []string{filename}, Options{
		Codename: "stable", Component: "main", Architecture: "arm64", ArchitectureSet: true, Now: fixedClock,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "dists/stable/main/binary-arm64/Packages"); err != nil {
		t.Fatalf("arm64 Packages missing: %v", err)
	}
}

func TestUploadExistingRepositoryVersionAndConflictRules(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	first := writeTestFile(t, directory, "example-one.deb", "one")
	second := writeTestFile(t, directory, "example-two.deb", "two")
	third := writeTestFile(t, directory, "example-three.deb", "three")
	conflict := writeTestFile(t, directory, "example-conflict.deb", "conflict")
	store := storage.NewMemoryStore("")
	specs := map[string]packageSpec{
		first:    {name: "example", version: "1.0", architecture: "amd64"},
		second:   {name: "example", version: "2.0", architecture: "amd64"},
		third:    {name: "example", version: "3.0", architecture: "amd64"},
		conflict: {name: "example", version: "3.0", architecture: "amd64"},
	}
	runner := Runner{Store: store, Inspect: inspector(specs)}
	base := Options{Codename: "stable", Component: "main", Now: fixedClock}
	if err := runner.Upload(ctx, []string{first}, base); err != nil {
		t.Fatal(err)
	}
	identical := base
	identical.FailIfExists = true
	if err := runner.Upload(ctx, []string{first}, identical); err != nil {
		t.Fatalf("identical upload was not skipped cleanly: %v", err)
	}
	preserve := base
	preserve.PreserveVersions = true
	if err := runner.Upload(ctx, []string{second}, preserve); err != nil {
		t.Fatal(err)
	}
	data, _ := store.Get(ctx, "dists/stable/main/binary-amd64/Packages")
	if strings.Count(string(data), "Package: example\n") != 2 {
		t.Fatalf("preserved Packages = %q", data)
	}
	if err := runner.Upload(ctx, []string{third}, base); err != nil {
		t.Fatal(err)
	}
	beforeConflict, _ := store.Get(ctx, "dists/stable/main/binary-amd64/Packages")
	if strings.Count(string(beforeConflict), "Package: example\n") != 1 || !strings.Contains(string(beforeConflict), "Version: 3.0\n") {
		t.Fatalf("replacement Packages = %q", beforeConflict)
	}
	fail := base
	fail.FailIfExists = true
	err := runner.Upload(ctx, []string{conflict}, fail)
	if !errors.Is(err, apt.ErrPackageConflict) || !strings.Contains(err.Error(), "prepare amd64 manifest") {
		t.Fatalf("conflict error = %v", err)
	}
	afterConflict, _ := store.Get(ctx, "dists/stable/main/binary-amd64/Packages")
	if !bytes.Equal(afterConflict, beforeConflict) {
		t.Fatal("conflict changed canonical Packages")
	}
}

func TestUploadCanSkipPackageObjects(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	filename := writeTestFile(t, directory, "external.deb", "external")
	store := storage.NewMemoryStore("")
	runner := Runner{Store: store, Inspect: inspector(map[string]packageSpec{
		filename: {name: "external", version: "1.0", architecture: "amd64"},
	})}
	if err := runner.Upload(ctx, []string{filename}, Options{
		Codename: "stable", Component: "main", SkipPackageUpload: true, Now: fixedClock,
	}); err != nil {
		t.Fatal(err)
	}
	pack, _ := runner.Inspect(ctx, filename)
	key, _ := pack.RepositoryFilename("stable")
	if _, err := store.Get(ctx, key); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("skipped package exists: %v", err)
	}
	if _, err := store.Get(ctx, "dists/stable/main/binary-amd64/Packages"); err != nil {
		t.Fatalf("Packages missing: %v", err)
	}
}

type packageSpec struct {
	name         string
	version      string
	architecture string
}

func inspector(specs map[string]packageSpec) Inspector {
	return func(_ context.Context, filename string) (*apt.Package, error) {
		spec, ok := specs[filename]
		if !ok {
			return nil, errors.New("unexpected package")
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return nil, err
		}
		pack := apt.NewPackage()
		pack.Name = spec.name
		pack.Version = spec.version
		pack.Architecture = spec.architecture
		pack.Filename = filename
		pack.Category = "utils"
		pack.Maintainer = "Example <example@example.test>"
		size := strconv.Itoa(len(data))
		md5Hash := md5.Sum(data)
		sha1Hash := sha1.Sum(data)
		sha256Hash := sha256.Sum256(data)
		md5Value := hex.EncodeToString(md5Hash[:])
		sha1Value := hex.EncodeToString(sha1Hash[:])
		sha256Value := hex.EncodeToString(sha256Hash[:])
		pack.Size = &size
		pack.MD5 = &md5Value
		pack.SHA1 = &sha1Value
		pack.SHA256 = &sha256Value
		return pack, nil
	}
}

func assertPublicationPhases(t *testing.T, transfers []string) {
	t.Helper()
	lastPhase := 0
	for _, filename := range transfers {
		phase := 0
		switch {
		case strings.HasPrefix(filename, "pool/"):
			phase = 0
		case strings.Contains(filename, "/by-hash/"):
			phase = 1
		case strings.HasSuffix(filename, "/Packages") || strings.HasSuffix(filename, "/Packages.gz"):
			phase = 2
		case strings.HasSuffix(filename, "/Release"):
			phase = 3
		default:
			t.Fatalf("unknown transfer phase for %q", filename)
		}
		if phase < lastPhase {
			t.Fatalf("transfer %q moved from phase %d back to %d: %#v", filename, lastPhase, phase, transfers)
		}
		lastPhase = phase
	}
}

func writeTestFile(t *testing.T, directory, name, contents string) string {
	t.Helper()
	filename := filepath.Join(directory, name)
	if err := os.WriteFile(filename, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return filename
}

func fixedClock() time.Time {
	return time.Date(2026, time.August, 4, 17, 0, 0, 0, time.UTC)
}

func buildTestDeb(t *testing.T, control string) []byte {
	t.Helper()
	controlTar := buildTestTar(t, "control", []byte(control))
	dataTar := buildTestTar(t, "", nil)
	type member struct {
		name string
		data []byte
	}
	members := []member{
		{name: "debian-binary", data: []byte("2.0\n")},
		{name: "control.tar", data: controlTar},
		{name: "data.tar", data: dataTar},
	}
	var output bytes.Buffer
	output.WriteString("!<arch>\n")
	for _, item := range members {
		name := item.name + "/"
		header := fmt.Sprintf("%-16s%-12d%-6d%-6d%-8o%-10d`\n", name, 0, 0, 0, 0o644, len(item.data))
		if len(header) != 60 {
			t.Fatalf("ar header length = %d", len(header))
		}
		output.WriteString(header)
		output.Write(item.data)
		if len(item.data)%2 != 0 {
			output.WriteByte('\n')
		}
	}
	return output.Bytes()
}

func buildTestTar(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	if name != "" {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
