package apt

import (
	"reflect"
	"testing"
)

func TestParseParagraphContinuationAndOrdering(t *testing.T) {
	paragraph := ParseParagraph("Package: example\r\nDescription: first\r\n second\r\n .\r\n fourth\r\nPriority: old\r\nPriority: new\r\ninvalid line\r\n")
	want := []Field{
		{Name: "Package", Value: "example"},
		{Name: "Description", Value: "first\nsecond\n\nfourth"},
		{Name: "Priority", Value: "new"},
	}
	if got := paragraph.Fields(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Fields() = %#v, want %#v", got, want)
	}

	value, ok := paragraph.Take("Description")
	if !ok || value != want[1].Value {
		t.Fatalf("Take(Description) = %q, %v", value, ok)
	}
	if got := paragraph.Fields(); !reflect.DeepEqual(got, []Field{want[0], want[2]}) {
		t.Fatalf("Fields after Take = %#v", got)
	}
}

// A dot continuation line marks a blank line only at exactly one space of
// indentation; deeper-indented dots are literal content.
func TestParseParagraphDotIndentRule(t *testing.T) {
	paragraph := ParseParagraph("Description: first\n  .\n")
	value, _ := paragraph.Get("Description")
	if value != "first\n." {
		t.Fatalf("Description = %q, want %q", value, "first\n.")
	}
}

func TestParseParagraphIgnoresOrphanContinuation(t *testing.T) {
	paragraph := ParseParagraph(" orphan\nPackage: example\n")
	if got := paragraph.Fields(); !reflect.DeepEqual(got, []Field{{Name: "Package", Value: "example"}}) {
		t.Fatalf("Fields() = %#v", got)
	}
}
