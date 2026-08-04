package apt

import (
	"reflect"
	"testing"
)

func TestParseDependencies(t *testing.T) {
	got := ParseDependencies("foo (>= 1.0), bar | baz, qux")
	want := []string{"foo (>= 1.0)", "bar | baz", "qux"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseDependencies() = %#v, want %#v", got, want)
	}
}

func TestNormalizeDependency(t *testing.T) {
	tests := []struct {
		name            string
		dependency      string
		ignoreIteration bool
		want            []string
		wantConflict    string
	}{
		{name: "plain", dependency: "foo", want: []string{"foo"}},
		{name: "convert operator", dependency: "Foo_bar > 1.0", want: []string{"foo-bar (>> 1.0)"}},
		{name: "preserve alternative", dependency: "Foo | bar", want: []string{"foo | bar"}},
		{name: "pessimistic", dependency: "foo (~> 1.2.3)", want: []string{"foo (>= 1.2.3)", "foo (<< 1.3.0)"}},
		{name: "single pessimistic version", dependency: "foo (~> 1)", want: []string{"foo (>= 1)", "foo (<< 0)"}},
		{name: "inequality becomes conflict", dependency: "foo (!= 1.0)", wantConflict: "foo (= 1.0)"},
		{name: "ignore iteration", dependency: "foo (= 1.2.3)", ignoreIteration: true, want: []string{"foo (>= 1.2.3)", "foo (<< 1.2.4)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, conflict := normalizeDependency(tt.dependency, tt.ignoreIteration)
			if !reflect.DeepEqual(got, tt.want) || conflict != tt.wantConflict {
				t.Fatalf("normalizeDependency() = %#v, %q; want %#v, %q", got, conflict, tt.want, tt.wantConflict)
			}
		})
	}
}
