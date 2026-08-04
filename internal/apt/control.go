package apt

import (
	"fmt"
	"strings"
	"unicode"
)

// Field is an ordered Debian control field. Order is significant when
// reproducing package indexes, so Paragraph does not use a map as its source
// of truth.
type Field struct {
	Name  string
	Value string
}

type Paragraph struct {
	fields []Field
	index  map[string]int
}

// ParseParagraph implements the lenient paragraph parsing used by the Ruby
// implementation. Unrecognized lines are ignored and duplicate fields replace
// their earlier value without changing field order.
func ParseParagraph(input string) *Paragraph {
	paragraph := &Paragraph{index: make(map[string]int)}
	var currentName string
	var currentValue string

	commit := func() {
		if currentName == "" {
			return
		}
		paragraph.set(currentName, currentValue)
	}

	input = strings.ReplaceAll(input, "\r\n", "\n")
	for _, line := range strings.Split(input, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if indent, rest, ok := continuation(line); ok {
			if currentName == "" {
				continue
			}
			if indent == 1 && rest == "." {
				currentValue += "\n"
				continue
			}
			if currentValue != "" {
				currentValue += "\n"
			}
			currentValue += rest
			continue
		}

		name, value, ok := controlField(line)
		if !ok {
			continue
		}
		commit()
		currentName = name
		currentValue = strings.TrimSpace(value)
	}
	commit()
	return paragraph
}

func (p *Paragraph) Fields() []Field {
	fields := make([]Field, len(p.fields))
	copy(fields, p.fields)
	return fields
}

func (p *Paragraph) Get(name string) (string, bool) {
	position, ok := p.index[name]
	if !ok {
		return "", false
	}
	return p.fields[position].Value, true
}

func (p *Paragraph) Take(name string) (string, bool) {
	position, ok := p.index[name]
	if !ok {
		return "", false
	}
	value := p.fields[position].Value
	p.fields = append(p.fields[:position], p.fields[position+1:]...)
	p.reindex()
	return value, true
}

func (p *Paragraph) set(name, value string) {
	if position, ok := p.index[name]; ok {
		p.fields[position].Value = value
		return
	}
	p.index[name] = len(p.fields)
	p.fields = append(p.fields, Field{Name: name, Value: value})
}

func (p *Paragraph) reindex() {
	p.index = make(map[string]int, len(p.fields))
	for position, field := range p.fields {
		p.index[field.Name] = position
	}
}

func continuation(line string) (indent int, rest string, ok bool) {
	for indent < len(line) && unicode.IsSpace(rune(line[indent])) {
		indent++
	}
	if indent == 0 || indent == len(line) {
		return 0, "", false
	}
	return indent, line[indent:], true
}

func controlField(line string) (name, value string, ok bool) {
	colon := strings.IndexByte(line, ':')
	if colon <= 0 {
		return "", "", false
	}
	name = line[:colon]
	for _, character := range name {
		if character != '-' && character != '_' && !unicode.IsLetter(character) && !unicode.IsDigit(character) {
			return "", "", false
		}
	}
	return name, line[colon+1:], true
}

func stringPointer(value string, present bool) *string {
	if !present {
		return nil
	}
	return &value
}

func requireField(paragraph *Paragraph, name string) (string, error) {
	value, ok := paragraph.Take(name)
	if !ok || value == "" {
		return "", fmt.Errorf("missing required control field %q", name)
	}
	return value, nil
}
