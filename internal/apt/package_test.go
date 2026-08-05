package apt

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPackageGoldenRoundTrip(t *testing.T) {
	input, err := os.ReadFile(filepath.Join("testdata", "Packages.golden"))
	if err != nil {
		t.Fatal(err)
	}
	pack, err := ParsePackage(string(input))
	if err != nil {
		t.Fatal(err)
	}
	if pack.Name != "discourse" || pack.Version != "0.9.8.3" || pack.Iteration != "1396474125.12e4179.wheezy" {
		t.Fatalf("parsed identity = %#v", pack)
	}
	if pack.Epoch != "" || pack.FullVersion() != "0.9.8.3-1396474125.12e4179.wheezy" {
		t.Fatalf("parsed version: epoch=%q full=%q", pack.Epoch, pack.FullVersion())
	}
	wantDescription := "A platform for community discussion. Free, open, simple.\nThe description can have a continuation line.\n\nAnd blank lines.\n\nIf it wants to."
	if pack.Description == nil || *pack.Description != wantDescription {
		t.Fatalf("Description = %v", pack.Description)
	}

	output, err := pack.Render("stable")
	if err != nil {
		t.Fatal(err)
	}
	if output != string(input) {
		firstDifference(t, string(input), output)
	}
}

func TestVersionParsingAndFullVersion(t *testing.T) {
	tests := []struct {
		full             string
		epoch            string
		version          string
		iteration        string
		iterationPresent bool
	}{
		{full: "0.9.8", version: "0.9.8"},
		{full: "2:0.9.8", epoch: "2", version: "0.9.8"},
		{full: "0.9.8-2", version: "0.9.8", iteration: "2", iterationPresent: true},
		{full: "2:0.9.8-2", epoch: "2", version: "0.9.8", iteration: "2", iterationPresent: true},
		{full: "1.2-beta-3", version: "1.2", iteration: "beta-3", iterationPresent: true},
		{full: "1.2-", version: "1.2", iterationPresent: true},
	}
	for _, tt := range tests {
		t.Run(tt.full, func(t *testing.T) {
			pack, err := ParsePackage("Package: example\nVersion: " + tt.full + "\n")
			if err != nil {
				t.Fatal(err)
			}
			if pack.Epoch != tt.epoch || pack.Version != tt.version || pack.Iteration != tt.iteration || pack.IterationPresent != tt.iterationPresent {
				t.Fatalf("parsed version = epoch %q, version %q, iteration %q, present %v", pack.Epoch, pack.Version, pack.Iteration, pack.IterationPresent)
			}
			if got := pack.FullVersion(); got != tt.full {
				t.Fatalf("FullVersion() = %q, want %q", got, tt.full)
			}
		})
	}
}

func TestInvalidVersions(t *testing.T) {
	for _, input := range []string{
		"Package: example\n",
		"Package: example\nVersion:\n",
	} {
		if _, err := ParsePackage(input); err == nil {
			t.Fatalf("ParsePackage(%q) succeeded", input)
		}
	}
}

func TestExtraFieldsAreNormalizedAndDeterministic(t *testing.T) {
	input := strings.Join([]string{
		"Package: example",
		"Version: 1.0",
		"Architecture: amd64",
		"Filename: pool/example.deb",
		"Description: example",
		"X-Private: first",
		"XB-Builder: second",
		"Multi-Arch: foreign",
		"XBCSS-Keep: original",
		"",
	}, "\n")
	pack, err := ParsePackage(input)
	if err != nil {
		t.Fatal(err)
	}
	want := []Field{
		{Name: "Private", Value: "first"},
		{Name: "Builder", Value: "second"},
		{Name: "Multi-Arch", Value: "foreign"},
		{Name: "XBCSS-Keep", Value: "original"},
	}
	if !reflect.DeepEqual(pack.ExtraFields, want) {
		t.Fatalf("ExtraFields = %#v, want %#v", pack.ExtraFields, want)
	}
	output, err := pack.Render("stable")
	if err != nil {
		t.Fatal(err)
	}
	positions := []string{"Private: first", "Builder: second", "Multi-Arch: foreign", "XBCSS-Keep: original"}
	last := -1
	for _, field := range positions {
		position := strings.Index(output, field)
		if position <= last {
			t.Fatalf("field %q was not rendered deterministically:\n%s", field, output)
		}
		last = position
	}
}

func TestMultiLineExtraFieldsRoundTrip(t *testing.T) {
	input := strings.Join([]string{
		"Package: example",
		"Version: 1.0",
		"Architecture: amd64",
		"Filename: pool/example.deb",
		"Description: example",
		"Tag: role::program, implemented-in::c,",
		" interface::commandline",
		" .",
		" network::client",
		"",
	}, "\n")
	pack, err := ParsePackage(input)
	if err != nil {
		t.Fatal(err)
	}
	output, err := pack.Render("stable")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(output, "Tag: role::program, implemented-in::c,\n interface::commandline\n .\n network::client\n") {
		t.Fatalf("multi-line extra field was not folded:\n%s", output)
	}
	reparsed, err := ParsePackage(output)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reparsed.ExtraFields, pack.ExtraFields) {
		t.Fatalf("extra fields did not survive a round trip:\n%#v\nwant %#v", reparsed.ExtraFields, pack.ExtraFields)
	}
	rerendered, err := reparsed.Render("stable")
	if err != nil {
		t.Fatal(err)
	}
	if rerendered != output {
		firstDifference(t, output, rerendered)
	}
}

func TestRelationshipFieldsRoundTripInTemplateOrder(t *testing.T) {
	input := strings.Join([]string{
		"Package: example",
		"Version: 1:2.0-3",
		"License: MIT",
		"Vendor: Example",
		"Architecture: amd64",
		"Maintainer: Example <example@example.test>",
		"Installed-Size: 10",
		"Depends: libc6 (>= 2.36), helper | alternative",
		"Conflicts: old-example",
		"Breaks: broken-example",
		"Pre-Depends: init-system-helpers",
		"Provides: virtual-example",
		"Replaces: replaced-example",
		"Recommends: recommended-example",
		"Suggests: suggested-example",
		"Enhances: enhanced-example",
		"Section: utils",
		"Origin: Example Repository",
		"Priority: optional",
		"Homepage: https://example.test",
		"Filename: pool/e/ex/example.deb",
		"Size: 100",
		"SHA1: sha1",
		"SHA256: sha256",
		"MD5sum: md5",
		"Description: example package",
		" continuation",
		"Multi-Arch: foreign",
		"",
	}, "\n")
	pack, err := ParsePackage(input)
	if err != nil {
		t.Fatal(err)
	}
	output, err := pack.Render("stable")
	if err != nil {
		t.Fatal(err)
	}
	if output != input {
		firstDifference(t, input, output)
	}
}

func TestNoDependsSuppressesDependencyField(t *testing.T) {
	pack, err := ParsePackage("Package: example\nVersion: 1.0\nDepends: dependency\nFilename: pool/example.deb\n")
	if err != nil {
		t.Fatal(err)
	}
	pack.NoDepends = true
	output, err := pack.Render("stable")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "Depends:") {
		t.Fatalf("NoDepends package rendered dependency:\n%s", output)
	}
}

func TestNewPackageMaintainerEnvironment(t *testing.T) {
	t.Setenv("DEBFULLNAME", "Example Maintainer")
	t.Setenv("DEBEMAIL", "maintainer@example.test")
	pack := NewPackage()
	if pack.Maintainer != "Example Maintainer <maintainer@example.test>" {
		t.Fatalf("Maintainer = %q", pack.Maintainer)
	}
	if pack.Architecture != "native" || pack.Category != "default" || pack.License != "unknown" || pack.Vendor != "none" {
		t.Fatalf("defaults = %#v", pack)
	}
}

func TestRepositoryFilename(t *testing.T) {
	pack := NewPackage()
	pack.Name = "example"
	pack.Filename = filepath.Join("tmp", "example_1.0_amd64.deb")
	got, err := pack.RepositoryFilename("stable")
	if err != nil {
		t.Fatal(err)
	}
	if got != "pool/stable/e/ex/example_1.0_amd64.deb" {
		t.Fatalf("RepositoryFilename() = %q", got)
	}

	explicit := "external/example.deb"
	pack.IndexFilename = &explicit
	got, err = pack.RepositoryFilename("stable")
	if err != nil || got != explicit {
		t.Fatalf("explicit RepositoryFilename() = %q, %v", got, err)
	}
}

func firstDifference(t *testing.T, want, got string) {
	t.Helper()
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	for line := 0; line < len(wantLines) || line < len(gotLines); line++ {
		var wantLine, gotLine string
		if line < len(wantLines) {
			wantLine = wantLines[line]
		}
		if line < len(gotLines) {
			gotLine = gotLines[line]
		}
		if wantLine != gotLine {
			t.Fatalf("render differs on line %d:\nwant %q\n got %q", line+1, wantLine, gotLine)
		}
	}
	t.Fatal("render differs")
}
