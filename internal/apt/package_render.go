package apt

import (
	"strings"
)

func (p *Package) Render(codename string) (string, error) {
	repositoryFilename, err := p.RepositoryFilename(codename)
	if err != nil {
		return "", err
	}

	dependencies := make([]string, 0, len(p.Dependencies))
	conflicts := make([]string, 0)
	for _, dependency := range p.Dependencies {
		normalized, conflict := normalizeDependency(dependency, p.IgnoreIterationInDependencies)
		dependencies = append(dependencies, normalized...)
		if conflict != "" {
			conflicts = append(conflicts, conflict)
		}
	}
	if p.Conflicts != nil {
		conflicts = append([]string{*p.Conflicts}, conflicts...)
	}

	var output strings.Builder
	writeField(&output, "Package", p.Name)
	writeField(&output, "Version", p.FullVersion())
	writeField(&output, "License", p.License)
	writeField(&output, "Vendor", p.Vendor)
	writeField(&output, "Architecture", p.Architecture)
	writeField(&output, "Maintainer", p.Maintainer)
	writeField(&output, "Installed-Size", pointerValue(p.InstalledSize))
	if len(p.Dependencies) > 0 && !p.NoDepends {
		writeField(&output, "Depends", strings.Join(dependencies, ", "))
	}
	if p.Conflicts != nil || len(conflicts) > 0 {
		writeField(&output, "Conflicts", strings.Join(conflicts, ", "))
	}
	writeOptionalField(&output, "Breaks", p.Breaks)
	writeOptionalField(&output, "Pre-Depends", p.PreDepends)
	writeOptionalField(&output, "Provides", p.Provides)
	writeOptionalField(&output, "Replaces", p.Replaces)
	writeOptionalField(&output, "Recommends", p.Recommends)
	writeOptionalField(&output, "Suggests", p.Suggests)
	writeOptionalField(&output, "Enhances", p.Enhances)
	writeField(&output, "Section", p.Category)
	writeOptionalField(&output, "Origin", p.Origin)
	writeField(&output, "Priority", pointerValue(p.Priority))
	if p.Homepage == nil {
		writeField(&output, "Homepage", "http://nourlgiven.example.com/")
	} else {
		writeField(&output, "Homepage", *p.Homepage)
	}
	writeField(&output, "Filename", repositoryFilename)
	writeOptionalField(&output, "Size", p.Size)
	writeOptionalField(&output, "SHA1", p.SHA1)
	writeOptionalField(&output, "SHA256", p.SHA256)
	writeOptionalField(&output, "MD5sum", p.MD5)
	writeDescription(&output, p.Description)
	for _, field := range p.ExtraFields {
		writeField(&output, field.Name, field.Value)
	}
	return output.String(), nil
}

func writeField(output *strings.Builder, name, value string) {
	output.WriteString(name)
	output.WriteString(": ")
	output.WriteString(value)
	output.WriteByte('\n')
}

func writeOptionalField(output *strings.Builder, name string, value *string) {
	if value != nil {
		writeField(output, name, *value)
	}
}

func writeDescription(output *strings.Builder, description *string) {
	value := "no description given"
	if description != nil {
		value = *description
	}
	lines := rubySplitLines(value)
	firstLine := ""
	if len(lines) > 0 {
		firstLine = lines[0]
	}
	writeField(output, "Description", firstLine)
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			output.WriteString(" .\n")
		} else {
			output.WriteByte(' ')
			output.WriteString(line)
			output.WriteByte('\n')
		}
	}
}

func rubySplitLines(value string) []string {
	if value == "" {
		return nil
	}
	lines := strings.Split(value, "\n")
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
