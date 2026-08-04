package cli

import (
	"strconv"
	"strings"

	"github.com/spf13/pflag"
)

func addNegatedBool(flags *pflag.FlagSet, name string, target *bool, usage string) {
	negated := &negatedBoolValue{target: target}
	flags.Var(negated, "no-"+name, "Disable "+usage)
	flag := flags.Lookup("no-" + name)
	flag.NoOptDefVal = "true"
	// A negated flag is an action, not independently configured state. Avoid
	// advertising the inverse of the target's default as the flag's default.
	flag.DefValue = "false"
}

type negatedBoolValue struct {
	target *bool
}

func (v *negatedBoolValue) String() string {
	if v == nil || v.target == nil {
		return "false"
	}
	if *v.target {
		return "false"
	}
	return "true"
}

func (v *negatedBoolValue) Set(value string) error {
	disable, err := strconv.ParseBool(value)
	if err != nil {
		return err
	}
	*v.target = !disable
	return nil
}

func (v *negatedBoolValue) Type() string { return "bool" }

// stringListValue accepts repeated flags, comma-separated values, and Thor's
// documented space-delimited --versions form.
type stringListValue struct {
	values *[]string
}

func (v *stringListValue) String() string {
	if v == nil || v.values == nil {
		return ""
	}
	return strings.Join(*v.values, ",")
}

func (v *stringListValue) Set(value string) error {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	*v.values = append(*v.values, parts...)
	return nil
}

func (v *stringListValue) Type() string { return "strings" }

const defaultSigningKey = "default"

// optionalStringArrayValue models Thor's repeatable --sign option, which can
// be supplied without a key ID to select GPG's default key.
type optionalStringArrayValue struct {
	enabled *bool
	values  *[]string
}

func (v *optionalStringArrayValue) String() string {
	if v == nil || v.values == nil {
		return ""
	}
	return strings.Join(*v.values, ",")
}

func (v *optionalStringArrayValue) Set(value string) error {
	*v.enabled = true
	if value != defaultSigningKey {
		*v.values = append(*v.values, value)
	}
	return nil
}

func (v *optionalStringArrayValue) Type() string { return "string" }
