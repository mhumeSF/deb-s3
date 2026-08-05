package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/deb-s3/deb-s3/internal/buildinfo"
	"github.com/deb-s3/deb-s3/internal/config"
	"github.com/deb-s3/deb-s3/internal/storage"
	"github.com/spf13/cobra"
)

var ErrPackagesMissing = errors.New("one or more packages are missing")

type storeFactory func(context.Context, config.Config) (storage.Store, error)

// Execute runs the CLI and converts command errors into process exit codes.
// Keeping os.Exit here, rather than inside commands, makes command behavior
// directly testable.
func Execute(args []string, stdout, stderr io.Writer) int {
	return executeCommand(NewRootCommand(), args, stdout, stderr)
}

func executeCommand(root *cobra.Command, args []string, stdout, stderr io.Writer) int {
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	if err := root.Execute(); err != nil {
		if errors.Is(err, ErrPackagesMissing) {
			return 1
		}
		// Command errors are printed as user-facing CLI output, which is why
		// commands deliberately phrase some of them as full sentences rather
		// than following Go error-string conventions.
		fmt.Fprintf(stderr, "!! %v\n", err)
		return 1
	}
	return 0
}

func NewRootCommand() *cobra.Command {
	return newRootCommand(func(ctx context.Context, cfg config.Config) (storage.Store, error) {
		return storage.NewS3Store(ctx, cfg)
	})
}

func newRootCommand(newStore storeFactory) *cobra.Command {
	cfg := config.New()

	root := &cobra.Command{
		Use:           "deb-s3",
		Short:         "Create and manage APT repositories on S3",
		Version:       buildinfo.Version,
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.Validate(); err != nil {
				return err
			}
			return nil
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true

	addGlobalFlags(root, &cfg)
	root.AddCommand(
		newUploadCommand(&cfg, newStore),
		newListCommand(&cfg, newStore),
		newShowCommand(&cfg, newStore),
		newExistsCommand(&cfg, newStore),
		newCopyCommand(&cfg, newStore),
		newDeleteCommand(&cfg, newStore),
		newVerifyCommand(&cfg, newStore),
		newCleanCommand(&cfg, newStore),
	)

	return root
}

func addGlobalFlags(root *cobra.Command, cfg *config.Config) {
	f := root.PersistentFlags()
	f.StringVarP(&cfg.Bucket, "bucket", "b", "", "Name of the S3 bucket")
	f.StringVar(&cfg.Prefix, "prefix", "", "Path prefix to use when storing objects")
	f.StringVarP(&cfg.Origin, "origin", "o", "", "Origin for the repository Release file")
	f.StringVar(&cfg.Suite, "suite", "", "Suite for the repository Release file")
	f.StringVarP(&cfg.Codename, "codename", "c", config.DefaultCodename, "Codename of the APT repository")
	f.StringVarP(&cfg.Component, "component", "m", config.DefaultComponent, "Component of the APT repository")
	f.StringVarP(&cfg.Section, "section", "s", "", "Deprecated alias for --component")
	_ = f.MarkHidden("section")
	f.StringVar(&cfg.AccessKeyID, "access-key-id", "", "Access key for connecting to S3")
	f.StringVar(&cfg.SecretAccessKey, "secret-access-key", "", "Secret key for connecting to S3")
	f.StringVar(&cfg.SessionToken, "session-token", "", "Optional session token for connecting to S3")
	f.StringVar(&cfg.Endpoint, "endpoint", "", "S3 API endpoint URL")
	f.StringVar(&cfg.S3Region, "s3-region", "", "Region for connecting to S3 (defaults to the AWS configuration chain, then "+config.DefaultRegion+")")
	f.BoolVar(&cfg.ForcePathStyle, "force-path-style", false, "Use S3 path-style addressing")
	addNegatedBool(f, "force-path-style", &cfg.ForcePathStyle, "S3 path-style addressing")
	f.StringVar(&cfg.ProxyURI, "proxy-uri", "", "Proxy URI for S3 requests")
	f.StringVarP(&cfg.Visibility, "visibility", "v", config.DefaultVisibility, "Object visibility: public, private, authenticated, bucket_owner, or nil")
	f.Var(&optionalStringArrayValue{enabled: &cfg.Sign, values: &cfg.SignKeys}, "sign", "GPG-sign Release metadata, optionally with a key ID (repeatable)")
	f.Lookup("sign").NoOptDefVal = defaultSigningKey
	f.StringVar(&cfg.GPGOptions, "gpg-options", "", "Additional options passed to GPG")
	f.StringVar(&cfg.GPGProvider, "gpg-provider", config.DefaultGPGProvider, "GPG executable to use")
	f.BoolVarP(&cfg.Encryption, "encryption", "e", false, "Use AES-256 S3 server-side encryption")
	addNegatedBool(f, "encryption", &cfg.Encryption, "S3 server-side encryption")
	f.BoolVarP(&cfg.Quiet, "quiet", "q", false, "Suppress informational output")
	addNegatedBool(f, "quiet", &cfg.Quiet, "quiet output")
	f.StringVarP(&cfg.CacheControl, "cache-control", "C", "", "Cache-Control header for S3 objects")
	f.BoolVar(&cfg.ByHash, "by-hash", false, "Publish APT indices by SHA-256 content hash")
	addNegatedBool(f, "by-hash", &cfg.ByHash, "APT by-hash index publication")
	f.BoolVar(&cfg.ChecksumWhenRequired, "checksum-when-required", false, "Send SDK request checksums only when required")
	addNegatedBool(f, "checksum-when-required", &cfg.ChecksumWhenRequired, "checksum-when-required mode")
}

func validateArchitecture(architecture string) error {
	return config.ValidateArchitecture(architecture)
}
