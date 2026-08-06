package apt

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

var versionPattern = regexp.MustCompile(`^(?:([0-9]+):)?(.+?)(?:-(.*))?$`)

type Package struct {
	Name             string
	Version          string
	Epoch            string
	Iteration        string
	IterationPresent bool
	Architecture     string
	Maintainer       string
	Category         string
	License          string
	Vendor           string

	Homepage      *string
	Origin        *string
	Priority      *string
	InstalledSize *string
	Description   *string
	IndexFilename *string
	Size          *string
	SHA1          *string
	SHA256        *string
	MD5           *string

	Dependencies []string
	PreDepends   *string
	Recommends   *string
	Suggests     *string
	Enhances     *string
	Breaks       *string
	Conflicts    *string
	Provides     *string
	Replaces     *string

	ExtraFields []Field

	Filename                      string
	NoDepends                     bool
	IgnoreIterationInDependencies bool
}

func NewPackage() *Package {
	hostname, _ := os.Hostname()
	maintainer := fmt.Sprintf("<%s@%s>", os.Getenv("USER"), hostname)
	if email, emailOK := os.LookupEnv("DEBEMAIL"); emailOK {
		if fullName, nameOK := os.LookupEnv("DEBFULLNAME"); nameOK {
			maintainer = fmt.Sprintf("%s <%s>", fullName, email)
		}
	}
	description := "no description given"
	return &Package{
		Architecture: "native",
		Maintainer:   maintainer,
		Category:     "default",
		License:      "unknown",
		Vendor:       "none",
		Description:  &description,
	}
}

func ParsePackage(input string) (*Package, error) {
	paragraph := ParseParagraph(input)
	fullVersion, err := requireField(paragraph, "Version")
	if err != nil {
		return nil, errors.New("unsupported version string ''")
	}
	match := versionPattern.FindStringSubmatch(fullVersion)
	if match == nil {
		return nil, fmt.Errorf("unsupported version string %q", fullVersion)
	}

	pack := NewPackage()
	pack.Epoch = match[1]
	pack.Version = match[2]
	pack.Iteration = match[3]
	versionWithoutEpoch := fullVersion
	if pack.Epoch != "" {
		versionWithoutEpoch = strings.TrimPrefix(fullVersion, pack.Epoch+":")
	}
	pack.IterationPresent = strings.Contains(versionWithoutEpoch, "-")
	pack.Architecture, _ = paragraph.Take("Architecture")
	pack.Category, _ = paragraph.Take("Section")
	if value, ok := paragraph.Take("License"); ok {
		pack.License = value
	}
	pack.Maintainer, _ = paragraph.Take("Maintainer")
	pack.Name, _ = paragraph.Take("Package")
	pack.Homepage = takePointer(paragraph, "Homepage")
	if value, ok := paragraph.Take("Vendor"); ok {
		pack.Vendor = value
	}
	pack.Priority = takePointer(paragraph, "Priority")
	pack.Origin = takePointer(paragraph, "Origin")
	pack.InstalledSize = takePointer(paragraph, "Installed-Size")
	pack.IndexFilename = takePointer(paragraph, "Filename")
	pack.SHA1 = takePointer(paragraph, "SHA1")
	pack.SHA256 = takePointer(paragraph, "SHA256")
	pack.MD5 = takePointer(paragraph, "MD5sum")
	pack.Size = takePointer(paragraph, "Size")
	pack.Description = takePointer(paragraph, "Description")

	if depends, ok := paragraph.Take("Depends"); ok {
		pack.Dependencies = ParseDependencies(depends)
	}
	pack.Recommends = takePointer(paragraph, "Recommends")
	pack.Suggests = takePointer(paragraph, "Suggests")
	pack.Enhances = takePointer(paragraph, "Enhances")
	pack.PreDepends = takePointer(paragraph, "Pre-Depends")
	pack.Breaks = takePointer(paragraph, "Breaks")
	pack.Conflicts = takePointer(paragraph, "Conflicts")
	pack.Provides = takePointer(paragraph, "Provides")
	pack.Replaces = takePointer(paragraph, "Replaces")

	for _, field := range paragraph.Fields() {
		field.Name = normalizeExtraFieldName(field.Name)
		pack.setExtraField(field)
	}
	return pack, nil
}

func (p *Package) FullVersion() string {
	if p.Epoch == "" && p.Version == "" && p.Iteration == "" && !p.IterationPresent {
		return ""
	}
	version := p.Version
	if p.Epoch != "" {
		version = p.Epoch + ":" + version
	}
	if p.IterationPresent || p.Iteration != "" {
		version += "-" + p.Iteration
	}
	return version
}

func (p *Package) RepositoryFilename(codename string) (string, error) {
	if p.IndexFilename != nil {
		return *p.IndexFilename, nil
	}
	if p.Name == "" {
		return "", errors.New("cannot derive package path without a package name")
	}
	if p.Filename == "" {
		return "", errors.New("cannot derive package path without a source filename")
	}
	prefix := p.Name
	if len(prefix) > 2 {
		prefix = prefix[:2]
	}
	// filepath.Base reads the local source path; path.Clean then checks the
	// resulting object key, which uses slash semantics on every platform. The
	// package name and source basename both come from the package itself, so a
	// hostile value must not be able to reshape the key by introducing a
	// separator or a traversal segment.
	derived := fmt.Sprintf("pool/%s/%s/%s/%s", codename, p.Name[:1], prefix, filepath.Base(p.Filename))
	if derived != path.Clean(derived) {
		return "", fmt.Errorf("package %q does not map to a pool path", p.Name)
	}
	return derived, nil
}

func takePointer(paragraph *Paragraph, name string) *string {
	value, ok := paragraph.Take(name)
	return stringPointer(value, ok)
}

func normalizeExtraFieldName(name string) string {
	if !strings.HasPrefix(name, "X") {
		return name
	}
	rest := name[1:]
	dash := strings.IndexByte(rest, '-')
	if dash < 0 || dash > 3 {
		return name
	}
	for _, character := range rest[:dash] {
		if character != 'B' && character != 'C' && character != 'S' {
			return name
		}
	}
	return rest[dash+1:]
}

func (p *Package) setExtraField(field Field) {
	for position := range p.ExtraFields {
		if p.ExtraFields[position].Name == field.Name {
			p.ExtraFields[position].Value = field.Value
			return
		}
	}
	p.ExtraFields = append(p.ExtraFields, field)
}
