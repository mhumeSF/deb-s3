package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mhumesf/deb-s3/internal/apt"
	"github.com/mhumesf/deb-s3/internal/config"
	repolock "github.com/mhumesf/deb-s3/internal/lock"
	"github.com/mhumesf/deb-s3/internal/maintenance"
	"github.com/mhumesf/deb-s3/internal/manage"
	"github.com/mhumesf/deb-s3/internal/query"
	"github.com/mhumesf/deb-s3/internal/signing"
	"github.com/mhumesf/deb-s3/internal/storage"
	"github.com/mhumesf/deb-s3/internal/upload"
	"github.com/spf13/cobra"
)

func newUploadCommand(cfg *config.Config, newStore storeFactory) *cobra.Command {
	var architecture string
	var preserveVersions bool
	var lock lockFlags
	var failIfExists bool
	var skipPackageUpload bool

	cmd := &cobra.Command{
		Use:   "upload FILES...",
		Short: "Upload packages and update an APT repository",
		Args:  cobra.MinimumNArgs(1),
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if err := validateArchitecture(architecture); err != nil {
				return err
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			signer, err := configuredSigner(cfg)
			if err != nil {
				return err
			}
			store, err := newStore(cmd.Context(), *cfg)
			if err != nil {
				return err
			}
			component := componentFor(cmd, cfg)
			origin, suite := originSuiteOverrides(cmd, cfg)
			progress := upload.Progress{}
			if !cfg.Quiet {
				progress.Log = func(message string) { fmt.Fprintf(cmd.OutOrStdout(), ">> %s\n", message) }
				progress.Transfer = func(filename string) { fmt.Fprintf(cmd.OutOrStdout(), "   -- Transferring %s\n", filename) }
			}
			runner := upload.Runner{Store: store, Progress: progress}
			return withRepositoryLock(cmd, cfg, store, lock, cfg.Codename, func(ctx context.Context) error {
				return runner.Upload(ctx, args, upload.Options{
					Codename: cfg.Codename, Component: component, Origin: origin, Suite: suite,
					Architecture: architecture, ArchitectureSet: cmd.Flags().Changed("arch"),
					PreserveVersions: preserveVersions, FailIfExists: failIfExists,
					SkipPackageUpload: skipPackageUpload, CacheControl: cfg.CacheControl,
					ByHash: byHashOption(cmd, cfg), Signer: signer,
				})
			})
		},
	}
	f := cmd.Flags()
	f.StringVarP(&architecture, "arch", "a", "", "Architecture of the package in the APT repository")
	f.BoolVarP(&preserveVersions, "preserve-versions", "p", false, "Preserve other package versions")
	addNegatedBool(f, "preserve-versions", &preserveVersions, "preserving other package versions")
	addLockFlags(f, &lock, "Lock the repository during the update")
	f.BoolVar(&failIfExists, "fail-if-exists", false, "Fail when the package already exists with different contents")
	addNegatedBool(f, "fail-if-exists", &failIfExists, "failure for existing conflicting packages")
	f.BoolVar(&skipPackageUpload, "skip-package-upload", false, "Update metadata without uploading package files")
	addNegatedBool(f, "skip-package-upload", &skipPackageUpload, "skipping package uploads")
	return cmd
}

func newListCommand(cfg *config.Config, newStore storeFactory) *cobra.Command {
	var long bool
	var architecture string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List packages in a codename and component",
		Args:  cobra.NoArgs,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			return validateArchitecture(architecture)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := newStore(cmd.Context(), *cfg)
			if err != nil {
				return err
			}
			component := componentFor(cmd, cfg)
			packages, err := (query.Repository{Store: store}).List(cmd.Context(), query.ListOptions{
				Codename: cfg.Codename, Component: component, Architecture: architecture,
			})
			if err != nil {
				return err
			}
			if long {
				records := make([]string, 0, len(packages))
				for _, pack := range packages {
					record, err := pack.Render(cfg.Codename)
					if err != nil {
						return err
					}
					records = append(records, record)
				}
				output := strings.Join(records, "\n")
				if !strings.HasSuffix(output, "\n") {
					output += "\n"
				}
				fmt.Fprint(cmd.OutOrStdout(), output)
				return nil
			}
			nameWidth, versionWidth := 0, 0
			for _, pack := range packages {
				nameWidth = max(nameWidth, len(pack.Name))
				versionWidth = max(versionWidth, len(pack.FullVersion()))
			}
			for _, pack := range packages {
				fmt.Fprintf(cmd.OutOrStdout(), "%-*s  %-*s  %s\n", nameWidth, pack.Name, versionWidth, pack.FullVersion(), pack.Architecture)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&long, "long", "l", false, "Show complete package records")
	cmd.Flags().StringVarP(&architecture, "arch", "a", "", "Architecture to list")
	return cmd
}

func newShowCommand(cfg *config.Config, newStore storeFactory) *cobra.Command {
	return &cobra.Command{
		Use:   "show PACKAGE VERSION ARCH",
		Short: "Show information about a package",
		Args:  architectureAt(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := newStore(cmd.Context(), *cfg)
			if err != nil {
				return err
			}
			pack, err := (query.Repository{Store: store}).Show(cmd.Context(), cfg.Codename, componentFor(cmd, cfg), args[2], args[0], args[1])
			if errors.Is(err, query.ErrPackageNotFound) {
				return errors.New("No such package found.")
			}
			if err != nil {
				return err
			}
			if cfg.Quiet {
				return nil
			}
			record, err := pack.Render(cfg.Codename)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), record)
			return nil
		},
	}
}

func newExistsCommand(cfg *config.Config, newStore storeFactory) *cobra.Command {
	return &cobra.Command{
		Use:   "exists PACKAGE [PACKAGE...] VERSION ARCH",
		Short: "Check whether packages exist in the repository",
		Args:  architectureAtLeast(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			architecture := args[len(args)-1]
			version := args[len(args)-2]
			names := args[:len(args)-2]
			store, err := newStore(cmd.Context(), *cfg)
			if err != nil {
				return err
			}
			matches, err := (query.Repository{Store: store}).Exists(cmd.Context(), cfg.Codename, componentFor(cmd, cfg), architecture, version, names)
			if err != nil {
				return err
			}
			missing := false
			for _, match := range matches {
				status := "Found"
				if !match.Found {
					status = "Missing"
					missing = true
				}
				if !cfg.Quiet {
					fmt.Fprintf(cmd.OutOrStdout(), ">> %s %s %s: %s\n", match.Name, match.Version, architecture, status)
				}
			}
			if missing {
				return ErrPackagesMissing
			}
			return nil
		},
	}
}

func componentFor(cmd *cobra.Command, cfg *config.Config) string {
	if cfg.Section == "" {
		return cfg.Component
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "===> WARNING: The --section/-s argument is deprecated, please use --component/-m.")
	return cfg.Section
}

func newCopyCommand(cfg *config.Config, newStore storeFactory) *cobra.Command {
	var architecture string
	var lock lockFlags
	var versions []string
	var preserveVersions bool
	failIfExists := true

	cmd := &cobra.Command{
		Use:   "copy PACKAGE TO_CODENAME TO_COMPONENT",
		Short: "Copy package metadata to another codename and component",
		Args:  cobra.ExactArgs(3),
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if architecture == "" {
				return errors.New("you must specify the architecture of the package to copy")
			}
			if err := validateArchitecture(architecture); err != nil {
				return err
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			signer, err := configuredSigner(cfg)
			if err != nil {
				return err
			}
			store, err := newStore(cmd.Context(), *cfg)
			if err != nil {
				return err
			}
			repository := manage.Repository{Store: store, Progress: manageProgress(cmd, cfg)}
			return withRepositoryLock(cmd, cfg, store, lock, args[1], func(ctx context.Context) error {
				err := repository.Copy(ctx, manage.CopyOptions{
					SourceCodename: cfg.Codename, SourceComponent: componentFor(cmd, cfg),
					DestinationCodename: args[1], DestinationComponent: args[2],
					Architecture: architecture, Package: args[0], Versions: versions,
					VersionsSet: cmd.Flags().Changed("versions"), PreserveVersions: preserveVersions,
					FailIfExists: failIfExists, CacheControl: cfg.CacheControl, ByHash: byHashOption(cmd, cfg), Signer: signer,
				})
				if errors.Is(err, manage.ErrNoPackagesFound) {
					return errors.New("No packages found in repository.")
				}
				return err
			})
		},
	}
	f := cmd.Flags()
	f.StringVarP(&architecture, "arch", "a", "", "Architecture of the package to copy")
	addLockFlags(f, &lock, "Lock the repository during the update")
	f.Var(&stringListValue{values: &versions}, "versions", "Versions to copy (space-delimited, comma-delimited, or repeated)")
	f.BoolVarP(&preserveVersions, "preserve-versions", "p", false, "Preserve other package versions")
	addNegatedBool(f, "preserve-versions", &preserveVersions, "preserving other package versions")
	f.BoolVar(&failIfExists, "fail-if-exists", true, "Fail when destination metadata already conflicts")
	addNegatedBool(f, "fail-if-exists", &failIfExists, "failure for existing conflicting packages")
	return cmd
}

func newDeleteCommand(cfg *config.Config, newStore storeFactory) *cobra.Command {
	var architecture string
	var lock lockFlags
	var versions []string
	cmd := &cobra.Command{
		Use:   "delete PACKAGE",
		Short: "Remove package entries from repository manifests",
		Args:  cobra.ExactArgs(1),
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if architecture == "" {
				return errors.New("you must specify the architecture of the package to remove")
			}
			if err := validateArchitecture(architecture); err != nil {
				return err
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			signer, err := configuredSigner(cfg)
			if err != nil {
				return err
			}
			store, err := newStore(cmd.Context(), *cfg)
			if err != nil {
				return err
			}
			origin, suite := originSuiteOverrides(cmd, cfg)
			versionsSet := cmd.Flags().Changed("versions")
			repository := manage.Repository{Store: store, Progress: manageProgress(cmd, cfg)}
			return withRepositoryLock(cmd, cfg, store, lock, cfg.Codename, func(ctx context.Context) error {
				err := repository.Delete(ctx, manage.DeleteOptions{
					Codename: cfg.Codename, Component: componentFor(cmd, cfg), Architecture: architecture,
					Package: args[0], Versions: versions, VersionsSet: versionsSet,
					Origin: origin, Suite: suite, CacheControl: cfg.CacheControl, ByHash: byHashOption(cmd, cfg), Signer: signer,
				})
				if errors.Is(err, manage.ErrNoPackagesDeleted) {
					if versionsSet {
						return fmt.Errorf("No packages were deleted. %s versions %s could not be found.", args[0], strings.Join(versions, ", "))
					}
					return fmt.Errorf("No packages were deleted. %s not found.", args[0])
				}
				return err
			})
		},
	}
	f := cmd.Flags()
	f.StringVarP(&architecture, "arch", "a", "", "Architecture to remove the package from")
	addLockFlags(f, &lock, "Lock the repository during the update")
	f.Var(&stringListValue{values: &versions}, "versions", "Versions to delete (space-delimited, comma-delimited, or repeated)")
	return cmd
}

func manageProgress(cmd *cobra.Command, cfg *config.Config) manage.Progress {
	progress := manage.Progress{Warn: func(message string) {
		fmt.Fprintln(cmd.ErrOrStderr(), message)
	}}
	if cfg.Quiet {
		return progress
	}
	progress.Log = func(message string) { fmt.Fprintf(cmd.OutOrStdout(), ">> %s\n", message) }
	progress.Detail = func(message string) { fmt.Fprintf(cmd.OutOrStdout(), "   -- %s\n", message) }
	progress.Transfer = func(filename string) { fmt.Fprintf(cmd.OutOrStdout(), "   -- Transferring %s\n", filename) }
	return progress
}

func byHashOption(cmd *cobra.Command, cfg *config.Config) *bool {
	persistent := cmd.Root().PersistentFlags()
	if !persistent.Changed("by-hash") && !persistent.Changed("no-by-hash") {
		return nil
	}
	return &cfg.ByHash
}

// originSuiteOverrides returns --origin/--suite values only when the flags
// were provided, so values already present in a Release file are preserved
// otherwise.
func originSuiteOverrides(cmd *cobra.Command, cfg *config.Config) (origin, suite *string) {
	persistent := cmd.Root().PersistentFlags()
	if persistent.Changed("origin") {
		origin = &cfg.Origin
	}
	if persistent.Changed("suite") {
		suite = &cfg.Suite
	}
	return origin, suite
}

func configuredSigner(cfg *config.Config) (apt.ReleaseSigner, error) {
	if !cfg.Sign {
		return nil, nil
	}
	options, err := signing.ParseOptions(cfg.GPGOptions)
	if err != nil {
		return nil, fmt.Errorf("parse GPG options: %w", err)
	}
	return signing.GPGSigner{
		Provider:     cfg.GPGProvider,
		Keys:         append([]string(nil), cfg.SignKeys...),
		ExtraOptions: options,
	}, nil
}

func withRepositoryLock(cmd *cobra.Command, cfg *config.Config, store storage.Store, lock lockFlags, codename string, operation func(context.Context) error) error {
	if !lock.enabled {
		return operation(cmd.Context())
	}
	if !cfg.Quiet {
		fmt.Fprintln(cmd.OutOrStdout(), ">> Checking for existing lock file")
		fmt.Fprintln(cmd.OutOrStdout(), ">> Locking repository for updates")
	}
	// --lock-timeout is the only bound on waiting, so the attempt count must not
	// cut a longer wait short.
	manager := repolock.Manager{Store: store, Options: repolock.Options{
		CacheControl: cfg.CacheControl,
		Timeout:      lock.timeout,
		MaxAttempts:  repolock.Unlimited,
		Reporter: func(info repolock.WaitInfo) {
			user, host := info.Current.User, info.Current.Host
			if user == "" {
				user = "unknown"
			}
			if host == "" {
				host = "unknown"
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Repository is locked by another user: %s at host %s\n", user, host)
			fmt.Fprintf(cmd.ErrOrStderr(), "Attempting to obtain a lock after %s.\n", info.Delay)
		},
	}}
	handle, err := manager.Acquire(cmd.Context(), codename)
	if err != nil {
		return err
	}
	operationErr := operation(cmd.Context())
	releaseContext, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), 30*time.Second)
	releaseErr := handle.Release(releaseContext)
	cancel()
	if releaseErr == nil && !cfg.Quiet {
		fmt.Fprintln(cmd.OutOrStdout(), ">> Lock released.")
	}
	return errors.Join(operationErr, releaseErr)
}

func newVerifyCommand(cfg *config.Config, newStore storeFactory) *cobra.Command {
	var fixManifests bool
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify that package files referenced by manifests exist",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			signer, err := configuredSigner(cfg)
			if err != nil {
				return err
			}
			store, err := newStore(cmd.Context(), *cfg)
			if err != nil {
				return err
			}
			origin, suite := originSuiteOverrides(cmd, cfg)
			progress := maintenance.Progress{}
			if !cfg.Quiet {
				progress.Log = func(message string) { fmt.Fprintf(cmd.OutOrStdout(), ">> %s\n", message) }
				progress.Transfer = func(filename string) { fmt.Fprintf(cmd.OutOrStdout(), "   -- Transferring %s\n", filename) }
				headers := make(map[string]bool)
				progress.Missing = func(architecture string, pack *apt.Package) {
					if !headers[architecture] {
						fmt.Fprint(cmd.OutOrStdout(), "   -- The following packages are missing:\n\n")
						headers[architecture] = true
					}
					record, renderErr := pack.Render(cfg.Codename)
					if renderErr != nil {
						fmt.Fprintf(cmd.OutOrStdout(), "Package: %s\nVersion: %s\n", pack.Name, pack.FullVersion())
						return
					}
					fmt.Fprint(cmd.OutOrStdout(), record)
					fmt.Fprintln(cmd.OutOrStdout())
				}
			}
			repository := maintenance.Repository{Store: store, Progress: progress}
			_, err = repository.Verify(cmd.Context(), maintenance.VerifyOptions{
				Codename: cfg.Codename, Component: componentFor(cmd, cfg), FixManifests: fixManifests,
				Origin: origin, Suite: suite, CacheControl: cfg.CacheControl, ByHash: byHashOption(cmd, cfg), Signer: signer,
			})
			return err
		},
	}
	cmd.Flags().BoolVarP(&fixManifests, "fix-manifests", "f", false, "Remove missing packages from manifests")
	addNegatedBool(cmd.Flags(), "fix-manifests", &fixManifests, "manifest repair")
	return cmd
}

func newCleanCommand(cfg *config.Config, newStore storeFactory) *cobra.Command {
	var lock lockFlags
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Delete unreferenced package files from the pool",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := newStore(cmd.Context(), *cfg)
			if err != nil {
				return err
			}
			progress := maintenance.Progress{}
			if !cfg.Quiet {
				progress.Log = func(message string) { fmt.Fprintf(cmd.OutOrStdout(), ">> %s\n", message) }
				progress.Detail = func(message string) { fmt.Fprintf(cmd.OutOrStdout(), "   -- %s\n", message) }
			}
			repository := maintenance.Repository{Store: store, Progress: progress}
			return withRepositoryLock(cmd, cfg, store, lock, cfg.Codename, func(ctx context.Context) error {
				_, err := repository.Clean(ctx, maintenance.CleanOptions{Codename: cfg.Codename})
				return err
			})
		},
	}
	addLockFlags(cmd.Flags(), &lock, "Lock the repository during cleanup")
	return cmd
}

func architectureAt(position int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(position)(cmd, args); err != nil {
			return err
		}
		return validateArchitecture(args[position-1])
	}
}

func architectureAtLeast(minimum int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.MinimumNArgs(minimum)(cmd, args); err != nil {
			return err
		}
		return validateArchitecture(args[len(args)-1])
	}
}
