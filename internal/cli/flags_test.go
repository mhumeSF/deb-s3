package cli

import "testing"

func TestNegatedBoolValue(t *testing.T) {
	enabled := true
	value := &negatedBoolValue{target: &enabled}
	if err := value.Set("true"); err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("Set(true) left target enabled")
	}
	if err := value.Set("false"); err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("Set(false) left target disabled")
	}
}

func TestStringListValue(t *testing.T) {
	var values []string
	value := &stringListValue{values: &values}
	for _, input := range []string{"1.0 1.1", "2.0,2.1", "3.0"} {
		if err := value.Set(input); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"1.0", "1.1", "2.0", "2.1", "3.0"}
	if len(values) != len(want) {
		t.Fatalf("values = %#v, want %#v", values, want)
	}
	for i := range want {
		if values[i] != want[i] {
			t.Fatalf("values = %#v, want %#v", values, want)
		}
	}
}

func TestOptionalStringArrayValue(t *testing.T) {
	var enabled bool
	var values []string
	value := &optionalStringArrayValue{enabled: &enabled, values: &values}
	if err := value.Set(defaultSigningKey); err != nil {
		t.Fatal(err)
	}
	if !enabled || len(values) != 0 {
		t.Fatalf("default signing key: enabled=%v values=%#v", enabled, values)
	}
	if err := value.Set("ABC123"); err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0] != "ABC123" {
		t.Fatalf("explicit signing key values=%#v", values)
	}
}
