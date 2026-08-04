package config

import (
	"errors"
	"fmt"
	"regexp"
)

const (
	DefaultCodename    = "stable"
	DefaultComponent   = "main"
	DefaultRegion      = "us-east-1"
	DefaultVisibility  = "public"
	DefaultGPGProvider = "gpg"
)

var architecturePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Config contains options shared by all deb-s3 commands. Command-specific
// options live beside their command definitions in internal/cli.
type Config struct {
	Bucket               string
	Prefix               string
	Origin               string
	Suite                string
	Codename             string
	Component            string
	Section              string
	AccessKeyID          string
	SecretAccessKey      string
	SessionToken         string
	Endpoint             string
	S3Region             string
	ForcePathStyle       bool
	ProxyURI             string
	Visibility           string
	Sign                 bool
	SignKeys             []string
	GPGOptions           string
	GPGProvider          string
	Encryption           bool
	Quiet                bool
	CacheControl         string
	ByHash               bool
	ChecksumWhenRequired bool
}

func New() Config {
	return Config{
		Codename:    DefaultCodename,
		Component:   DefaultComponent,
		S3Region:    DefaultRegion,
		Visibility:  DefaultVisibility,
		GPGProvider: DefaultGPGProvider,
	}
}

// Validate checks configuration that is common to every executable command.
func (c Config) Validate() error {
	if c.Bucket == "" {
		return errors.New("no value provided for required option '--bucket'")
	}

	if (c.AccessKeyID == "") != (c.SecretAccessKey == "") {
		return errors.New("if you specify one of --access-key-id or --secret-access-key, you must specify the other")
	}

	switch c.Visibility {
	case "public", "private", "authenticated", "bucket_owner", "nil":
	default:
		return fmt.Errorf("invalid visibility %q: expected public, private, authenticated, bucket_owner, or nil", c.Visibility)
	}

	if c.Codename == "" {
		return errors.New("codename cannot be empty")
	}
	if c.Component == "" && c.Section == "" {
		return errors.New("component cannot be empty")
	}

	return nil
}

// ValidateArchitecture accepts Debian architecture names and the special
// value "all". An empty value means that the package will supply it.
func ValidateArchitecture(architecture string) error {
	if architecture == "" {
		return nil
	}
	if !architecturePattern.MatchString(architecture) {
		return fmt.Errorf("architecture %q must contain only lowercase letters, numbers, and hyphens", architecture)
	}
	return nil
}
