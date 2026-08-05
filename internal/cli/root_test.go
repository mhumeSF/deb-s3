package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/deb-s3/deb-s3/internal/apt"
	"github.com/deb-s3/deb-s3/internal/config"
	"github.com/deb-s3/deb-s3/internal/storage"
	"github.com/spf13/cobra"
)

func TestCommandSurface(t *testing.T) {
	root := NewRootCommand()
	wantCommands := []string{"upload", "list", "show", "exists", "copy", "delete", "verify", "clean"}
	for _, name := range wantCommands {
		command, _, err := root.Find([]string{name})
		if err != nil || command == nil || command.Name() != name {
			t.Errorf("command %q not found: command=%v error=%v", name, command, err)
		}
	}
}

func TestDevelopmentVersion(t *testing.T) {
	root := NewRootCommand()
	if root.Version != "devel" {
		t.Fatalf("root version = %q, want devel", root.Version)
	}
	code, stdout, stderr := executeTestRoot(root, "--version")
	if code != 0 || stderr != "" || stdout != "deb-s3 version devel\n" {
		t.Fatalf("--version: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestGlobalFlagSurface(t *testing.T) {
	root := NewRootCommand()
	want := []string{
		"bucket", "prefix", "origin", "suite", "codename", "component",
		"section", "access-key-id", "secret-access-key", "session-token",
		"endpoint", "s3-region", "force-path-style", "no-force-path-style",
		"proxy-uri", "visibility", "sign", "gpg-options", "gpg-provider",
		"encryption", "no-encryption", "quiet", "no-quiet", "cache-control",
		"by-hash", "no-by-hash",
		"checksum-when-required", "no-checksum-when-required",
	}
	for _, name := range want {
		if root.PersistentFlags().Lookup(name) == nil {
			t.Errorf("global flag --%s not found", name)
		}
	}
}

func TestFlagAliasesAndDefaults(t *testing.T) {
	root := NewRootCommand()
	wantAliases := map[string]string{
		"bucket": "b", "cache-control": "C", "codename": "c",
		"component": "m", "encryption": "e", "origin": "o",
		"quiet": "q", "visibility": "v",
	}
	for name, shorthand := range wantAliases {
		flag := root.PersistentFlags().Lookup(name)
		if flag == nil {
			t.Errorf("--%s not found", name)
			continue
		}
		if flag.Shorthand != shorthand {
			t.Errorf("--%s shorthand = %q, want %q", name, flag.Shorthand, shorthand)
		}
	}

	wantDefaults := map[string]string{
		"codename": "stable", "component": "main", "s3-region": "",
		"visibility": "public", "gpg-provider": "gpg",
	}
	for name, value := range wantDefaults {
		flag := root.PersistentFlags().Lookup(name)
		if flag == nil {
			t.Errorf("--%s not found", name)
			continue
		}
		if flag.DefValue != value {
			t.Errorf("--%s default = %q, want %q", name, flag.DefValue, value)
		}
	}
}

func TestCommandFlagSurface(t *testing.T) {
	tests := map[string][]string{
		"upload": {"arch", "preserve-versions", "no-preserve-versions", "lock", "no-lock", "fail-if-exists", "no-fail-if-exists", "skip-package-upload", "no-skip-package-upload"},
		"list":   {"long", "arch"},
		"copy":   {"arch", "lock", "no-lock", "versions", "preserve-versions", "no-preserve-versions", "fail-if-exists", "no-fail-if-exists"},
		"delete": {"arch", "lock", "no-lock", "versions"},
		"verify": {"fix-manifests", "no-fix-manifests"},
		"clean":  {"lock", "no-lock"},
	}

	root := NewRootCommand()
	for commandName, flags := range tests {
		command, _, err := root.Find([]string{commandName})
		if err != nil {
			t.Fatalf("find %s: %v", commandName, err)
		}
		for _, flag := range flags {
			if command.Flags().Lookup(flag) == nil {
				t.Errorf("%s flag --%s not found", commandName, flag)
			}
		}
	}
}

func TestExecuteExitCodes(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStderr string
	}{
		{name: "help", args: []string{"--help"}, wantCode: 0},
		{name: "missing bucket", args: []string{"list"}, wantCode: 1, wantStderr: "--bucket"},
		{name: "invalid visibility", args: []string{"--bucket", "repo", "--visibility", "world", "list"}, wantCode: 1, wantStderr: "invalid visibility"},
		{name: "incomplete credentials", args: []string{"--bucket", "repo", "--access-key-id", "key", "list"}, wantCode: 1, wantStderr: "must specify the other"},
		{name: "invalid gpg options", args: []string{"--bucket", "repo", "verify", "--sign", "--gpg-options", "\"unterminated"}, wantCode: 1, wantStderr: "unterminated quote"},
		{name: "missing upload files", args: []string{"--bucket", "repo", "upload"}, wantCode: 1, wantStderr: "requires at least 1 arg"},
		{name: "missing upload pattern", args: []string{"--bucket", "repo", "upload", "missing-*.deb"}, wantCode: 1, wantStderr: "doesn't exist"},
		{name: "delete validates gpg options before storage", args: []string{"--bucket", "repo", "delete", "--arch", "amd64", "--sign", "--gpg-options", "'unterminated", "example"}, wantCode: 1, wantStderr: "unterminated quote"},
		{name: "delete requires arch", args: []string{"--bucket", "repo", "delete", "example"}, wantCode: 1, wantStderr: "must specify the architecture"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := Execute(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("Execute() code = %d, want %d; stderr=%q", code, tt.wantCode, stderr.String())
			}
			if tt.wantStderr != "" && !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want containing %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestVerifySignsReleaseAndPublishesInReleaseLast(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX executable script")
	}
	directory := t.TempDir()
	provider := filepath.Join(directory, "fake-gpg")
	script := `#!/bin/sh
output=
action=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) shift; output=$1 ;;
    --clearsign) action=clear ;;
    --detach-sign) action=detached ;;
    --verify) exit 0 ;;
  esac
  shift
done
printf '%s signature\n' "$action" > "$output"
`
	if err := os.WriteFile(provider, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	base := maintenanceCommandStore(t)
	store := &cliLockRecordingStore{Store: base}
	newStore := func(context.Context, config.Config) (storage.Store, error) { return store, nil }
	code, _, stderr := executeTestRoot(newRootCommand(newStore),
		"--bucket", "repo", "--gpg-provider", provider, "verify", "--sign=first-key", "--sign=second-key",
	)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	want := []string{"dists/stable/Release", "dists/stable/Release.gpg", "dists/stable/InRelease"}
	if !reflect.DeepEqual(store.putKeys, want) {
		t.Fatalf("signed publication order = %#v, want %#v", store.putKeys, want)
	}
	for filename, contents := range map[string]string{
		"dists/stable/Release.gpg": "detached signature\n",
		"dists/stable/InRelease":   "clear signature\n",
	} {
		data, err := base.Get(context.Background(), filename)
		if err != nil || string(data) != contents {
			t.Fatalf("%s = %q, %v", filename, data, err)
		}
	}
}

func TestAWSDefaultRegionDoesNotOverrideConfigChain(t *testing.T) {
	// Region env vars are resolved by the AWS SDK's configuration chain with
	// its documented precedence; the CLI must not turn them into an explicit
	// --s3-region value, which would override profile configuration.
	t.Setenv("AWS_DEFAULT_REGION", "eu-west-1")
	root := NewRootCommand()
	flag := root.PersistentFlags().Lookup("s3-region")
	if flag == nil {
		t.Fatal("--s3-region not found")
	}
	if flag.DefValue != "" {
		t.Fatalf("--s3-region default = %q, want empty", flag.DefValue)
	}
}

func TestReadOnlyCommands(t *testing.T) {
	store, packages := readOnlyCommandStore(t)
	newStore := func(context.Context, config.Config) (storage.Store, error) { return store, nil }

	t.Run("list short", func(t *testing.T) {
		code, stdout, stderr := executeTestRoot(newRootCommand(newStore), "--bucket", "repo", "list")
		if code != 0 || stderr != "" {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		want := "alpha     1:2.0-3  amd64\n" +
			"z         10       amd64\n" +
			"portable  4.0-1    all\n"
		if stdout != want {
			t.Fatalf("stdout = %q, want %q", stdout, want)
		}
	})

	t.Run("list architecture", func(t *testing.T) {
		code, stdout, stderr := executeTestRoot(newRootCommand(newStore), "--bucket", "repo", "list", "--arch", "arm64")
		if code != 0 || stderr != "" || stdout != "portable  4.0-1  all\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})

	t.Run("list all architecture alias", func(t *testing.T) {
		code, stdout, _ := executeTestRoot(newRootCommand(newStore), "--bucket", "repo", "list", "--arch", "all")
		if code != 0 || strings.Count(stdout, "\n") != 3 {
			t.Fatalf("code=%d stdout=%q", code, stdout)
		}
	})

	t.Run("list long", func(t *testing.T) {
		code, stdout, stderr := executeTestRoot(newRootCommand(newStore), "--bucket", "repo", "list", "--long")
		if code != 0 || stderr != "" {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		records := make([]string, 0, len(packages))
		for _, pack := range packages {
			record, err := pack.Render("stable")
			if err != nil {
				t.Fatal(err)
			}
			records = append(records, record)
		}
		want := strings.Join(records, "\n")
		if stdout != want {
			t.Fatalf("long stdout differs:\ngot:\n%s\nwant:\n%s", stdout, want)
		}
	})

	t.Run("show and quiet show", func(t *testing.T) {
		want, err := packages[0].Render("stable")
		if err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := executeTestRoot(newRootCommand(newStore), "--bucket", "repo", "show", "alpha", "1:2.0-3", "amd64")
		if code != 0 || stdout != want || stderr != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		code, stdout, stderr = executeTestRoot(newRootCommand(newStore), "--bucket", "repo", "--quiet", "show", "alpha", "1:2.0-3", "amd64")
		if code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("quiet code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})

	t.Run("show missing", func(t *testing.T) {
		code, stdout, stderr := executeTestRoot(newRootCommand(newStore), "--bucket", "repo", "show", "alpha", "2.0-3", "amd64")
		if code != 1 || stdout != "" || stderr != "!! No such package found.\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})

	t.Run("exists batch and quiet status", func(t *testing.T) {
		args := []string{"--bucket", "repo", "exists", "alpha", "missing", "1:2.0-3", "amd64"}
		code, stdout, stderr := executeTestRoot(newRootCommand(newStore), args...)
		want := ">> alpha 1:2.0-3 amd64: Found\n>> missing 1:2.0-3 amd64: Missing\n"
		if code != 1 || stdout != want || stderr != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		quietArgs := append([]string{"--quiet"}, args...)
		code, stdout, stderr = executeTestRoot(newRootCommand(newStore), quietArgs...)
		if code != 1 || stdout != "" || stderr != "" {
			t.Fatalf("quiet code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})

	t.Run("exists success", func(t *testing.T) {
		code, stdout, stderr := executeTestRoot(newRootCommand(newStore), "--bucket", "repo", "exists", "alpha", "1:2.0-3", "amd64")
		if code != 0 || stdout != ">> alpha 1:2.0-3 amd64: Found\n" || stderr != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})
}

func TestCopyCommand(t *testing.T) {
	store := mutationCommandStore(t)
	newStore := func(context.Context, config.Config) (storage.Store, error) { return store, nil }
	beforePool, _ := store.List(context.Background(), "pool/")
	code, stdout, stderr := executeTestRoot(newRootCommand(newStore),
		"--bucket", "repo", "copy", "--arch", "amd64", "--versions", "1:2.0-3",
		"alpha", "testing", "contrib",
	)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, line := range []string{
		">> Versions to copy: 1:2.0-3\n",
		">> Retrieving existing manifests\n",
		"   -- Transferring dists/testing/contrib/binary-amd64/Packages\n",
		"   -- Transferring dists/testing/Release\n",
		">> Copy complete.\n",
	} {
		if !strings.Contains(stdout, line) {
			t.Fatalf("copy stdout missing %q:\n%s", line, stdout)
		}
	}
	manifest, err := apt.RetrieveManifest(context.Background(), store, apt.ManifestOptions{
		Codename: "testing", Component: "contrib", Architecture: "amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Packages) != 2 || manifest.Packages[1].Name != "alpha" || manifest.Packages[1].FullVersion() != "1:2.0-3" {
		t.Fatalf("destination Packages = %#v", manifest.Packages)
	}
	afterPool, _ := store.List(context.Background(), "pool/")
	if !reflect.DeepEqual(cliObjectKeys(beforePool), cliObjectKeys(afterPool)) {
		t.Fatalf("copy changed pool: before=%#v after=%#v", cliObjectKeys(beforePool), cliObjectKeys(afterPool))
	}
}

func TestCopyCommandAllVersionsWarningAndNoMatch(t *testing.T) {
	t.Run("warning", func(t *testing.T) {
		store := mutationCommandStore(t)
		newStore := func(context.Context, config.Config) (storage.Store, error) { return store, nil }
		code, _, stderr := executeTestRoot(newRootCommand(newStore),
			"--bucket", "repo", "copy", "--arch", "amd64", "alpha", "testing", "contrib",
		)
		if code != 0 || stderr != "===> WARNING: Copying all versions of alpha\n" {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
	})

	t.Run("no match", func(t *testing.T) {
		store := mutationCommandStore(t)
		newStore := func(context.Context, config.Config) (storage.Store, error) { return store, nil }
		code, _, stderr := executeTestRoot(newRootCommand(newStore),
			"--bucket", "repo", "copy", "--arch", "amd64", "--versions", "9.9", "alpha", "testing", "contrib",
		)
		if code != 1 || stderr != "!! No packages found in repository.\n" {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
	})
}

func TestDeleteCommandAcrossArchitecturesAndNoMatch(t *testing.T) {
	t.Run("multi architecture", func(t *testing.T) {
		store := mutationCommandStore(t)
		newStore := func(context.Context, config.Config) (storage.Store, error) { return store, nil }
		beforePool, _ := store.List(context.Background(), "pool/")
		code, stdout, stderr := executeTestRoot(newRootCommand(newStore),
			"--bucket", "repo", "delete", "--arch", "all", "--versions", "1:2.0-3", "alpha",
		)
		if code != 0 || stderr != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if strings.Count(stdout, "Deleting alpha version 1:2.0-3") != 2 || !strings.Contains(stdout, ">> Update complete.\n") {
			t.Fatalf("delete stdout = %q", stdout)
		}
		for _, architecture := range []string{"amd64", "arm64"} {
			manifest, err := apt.RetrieveManifest(context.Background(), store, apt.ManifestOptions{
				Codename: "stable", Component: "main", Architecture: architecture,
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, pack := range manifest.Packages {
				if pack.Name == "alpha" && pack.FullVersion() == "1:2.0-3" {
					t.Fatalf("%s still contains deleted package", architecture)
				}
			}
		}
		afterPool, _ := store.List(context.Background(), "pool/")
		if !reflect.DeepEqual(cliObjectKeys(beforePool), cliObjectKeys(afterPool)) {
			t.Fatal("delete changed pool objects")
		}
	})

	t.Run("no match", func(t *testing.T) {
		store := mutationCommandStore(t)
		newStore := func(context.Context, config.Config) (storage.Store, error) { return store, nil }
		code, stdout, stderr := executeTestRoot(newRootCommand(newStore),
			"--bucket", "repo", "delete", "--arch", "all", "missing",
		)
		if code != 1 || !strings.Contains(stderr, "===> WARNING: Deleting all versions of missing\n") || !strings.Contains(stderr, "!! No packages were deleted. missing not found.\n") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if strings.Count(stdout, "No packages were deleted. missing not found in arch") != 2 {
			t.Fatalf("no-match stdout = %q", stdout)
		}
	})
}

func TestVerifyCommandReadOnlyAndRepair(t *testing.T) {
	t.Run("read only", func(t *testing.T) {
		store := maintenanceCommandStore(t)
		newStore := func(context.Context, config.Config) (storage.Store, error) { return store, nil }
		before, _ := store.Get(context.Background(), "dists/stable/main/binary-amd64/Packages")
		code, stdout, stderr := executeTestRoot(newRootCommand(newStore), "--bucket", "repo", "verify")
		if code != 0 || stderr != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		for _, text := range []string{
			">> Retrieving existing manifests\n",
			">> Checking for missing packages in: stable/main amd64\n",
			"   -- The following packages are missing:\n\n",
			"Package: missing\n",
		} {
			if !strings.Contains(stdout, text) {
				t.Fatalf("verify stdout missing %q:\n%s", text, stdout)
			}
		}
		after, _ := store.Get(context.Background(), "dists/stable/main/binary-amd64/Packages")
		if !bytes.Equal(before, after) {
			t.Fatal("read-only verify changed Packages")
		}
	})

	t.Run("quiet read only", func(t *testing.T) {
		store := maintenanceCommandStore(t)
		newStore := func(context.Context, config.Config) (storage.Store, error) { return store, nil }
		code, stdout, stderr := executeTestRoot(newRootCommand(newStore), "--bucket", "repo", "--quiet", "verify")
		if code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})

	t.Run("repair", func(t *testing.T) {
		store := maintenanceCommandStore(t)
		newStore := func(context.Context, config.Config) (storage.Store, error) { return store, nil }
		code, stdout, stderr := executeTestRoot(newRootCommand(newStore), "--bucket", "repo", "verify", "--fix-manifests")
		if code != 0 || stderr != "" || !strings.Contains(stdout, ">> Removing 1 package(s) from the manifest...\n") || !strings.Contains(stdout, ">> Update complete.\n") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		manifest, err := apt.RetrieveManifest(context.Background(), store, apt.ManifestOptions{
			Codename: "stable", Component: "main", Architecture: "amd64",
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(manifest.Packages) != 1 || manifest.Packages[0].Name != "present" {
			t.Fatalf("repaired Packages = %#v", manifest.Packages)
		}
		if _, err := store.Get(context.Background(), "pool/stable/unreferenced.deb"); err != nil {
			t.Fatalf("repair touched unrelated pool object: %v", err)
		}
	})
}

func TestCleanCommandDeletesOnlyUnreferencedPoolObjects(t *testing.T) {
	store := maintenanceCommandStore(t)
	newStore := func(context.Context, config.Config) (storage.Store, error) { return store, nil }
	code, stdout, stderr := executeTestRoot(newRootCommand(newStore), "--bucket", "repo", "clean")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, text := range []string{
		">> Retrieving existing manifests\n",
		">> Searching for unreferenced packages\n",
		"   -- Deleting pool/stable/unreferenced.deb\n",
	} {
		if !strings.Contains(stdout, text) {
			t.Fatalf("clean stdout missing %q:\n%s", text, stdout)
		}
	}
	if _, err := store.Get(context.Background(), "pool/stable/unreferenced.deb"); err == nil {
		t.Fatal("clean retained unreferenced pool object")
	}
	for _, key := range []string{
		"pool/stable/present.deb",
		"pool/stable-old/unreferenced.deb",
		"dists/stable/main/binary-amd64/by-hash/SHA256/keep",
	} {
		if _, err := store.Get(context.Background(), key); err != nil {
			t.Fatalf("clean removed %q: %v", key, err)
		}
	}
}

func TestMutatingCommandsReleaseRepositoryLocks(t *testing.T) {
	t.Run("copy locks destination", func(t *testing.T) {
		base := mutationCommandStore(t)
		store := &cliLockRecordingStore{Store: base}
		newStore := func(context.Context, config.Config) (storage.Store, error) { return store, nil }
		code, stdout, stderr := executeTestRoot(newRootCommand(newStore),
			"--bucket", "repo", "copy", "--lock", "--arch", "amd64", "--versions", "1:2.0-3",
			"alpha", "testing", "contrib",
		)
		if code != 0 || stderr != "" || !strings.Contains(stdout, ">> Lock released.\n") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !containsString(store.putKeys, "dists/testing/lockfile") || containsString(store.putKeys, "dists/stable/lockfile") {
			t.Fatalf("copy lock puts = %#v", store.putKeys)
		}
		if _, err := base.Get(context.Background(), "dists/testing/lockfile"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("destination lock remains: %v", err)
		}
	})

	t.Run("operation error releases lock", func(t *testing.T) {
		base := mutationCommandStore(t)
		store := &cliLockRecordingStore{Store: base}
		newStore := func(context.Context, config.Config) (storage.Store, error) { return store, nil }
		code, stdout, _ := executeTestRoot(newRootCommand(newStore),
			"--bucket", "repo", "delete", "--lock", "--arch", "amd64", "missing",
		)
		if code != 1 || !strings.Contains(stdout, ">> Lock released.\n") {
			t.Fatalf("code=%d stdout=%q", code, stdout)
		}
		if _, err := base.Get(context.Background(), "dists/stable/lockfile"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("lock remains after delete error: %v", err)
		}
	})

	t.Run("upload inspection error releases lock", func(t *testing.T) {
		filename := filepath.Join(t.TempDir(), "invalid.deb")
		if err := os.WriteFile(filename, []byte("not a deb"), 0o600); err != nil {
			t.Fatal(err)
		}
		base := storage.NewMemoryStore("prefix")
		store := &cliLockRecordingStore{Store: base}
		newStore := func(context.Context, config.Config) (storage.Store, error) { return store, nil }
		code, stdout, _ := executeTestRoot(newRootCommand(newStore), "--bucket", "repo", "upload", "--lock", filename)
		if code != 1 || !strings.Contains(stdout, ">> Lock released.\n") {
			t.Fatalf("code=%d stdout=%q", code, stdout)
		}
		if _, err := base.Get(context.Background(), "dists/stable/lockfile"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("lock remains after upload error: %v", err)
		}
	})

	t.Run("clean success releases lock", func(t *testing.T) {
		base := maintenanceCommandStore(t)
		store := &cliLockRecordingStore{Store: base}
		newStore := func(context.Context, config.Config) (storage.Store, error) { return store, nil }
		code, stdout, stderr := executeTestRoot(newRootCommand(newStore), "--bucket", "repo", "clean", "--lock")
		if code != 0 || stderr != "" || !strings.Contains(stdout, ">> Lock released.\n") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if _, err := base.Get(context.Background(), "dists/stable/lockfile"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("clean lock remains: %v", err)
		}
	})
}

func executeTestRoot(root *cobra.Command, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := executeCommand(root, args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func readOnlyCommandStore(t *testing.T) (*storage.MemoryStore, []*apt.Package) {
	t.Helper()
	ctx := context.Background()
	store := storage.NewMemoryStore("repository-prefix")
	packages := []*apt.Package{
		cliPackage("alpha", "1", "2.0", "3", "amd64", "pool/alpha.deb"),
		cliPackage("z", "", "10", "", "amd64", "pool/z.deb"),
		cliPackage("portable", "", "4.0", "1", "all", "pool/portable.deb"),
	}
	manifests := []*apt.Manifest{
		apt.NewManifest(store, apt.ManifestOptions{Codename: "stable", Component: "main", Architecture: "amd64"}),
		apt.NewManifest(store, apt.ManifestOptions{Codename: "stable", Component: "main", Architecture: "arm64"}),
	}
	manifests[0].Packages = packages[:2]
	manifests[1].Packages = packages[2:]
	release := apt.NewRelease(store, apt.ReleaseOptions{Codename: "stable", Now: func() time.Time {
		return time.Date(2026, time.August, 4, 18, 30, 0, 0, time.UTC)
	}})
	for _, manifest := range manifests {
		if err := manifest.Publish(ctx, nil); err != nil {
			t.Fatal(err)
		}
		release.UpdateManifest(manifest)
	}
	if err := release.Publish(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(release.Architectures, []string{"amd64", "arm64"}) {
		t.Fatalf("seed architectures = %#v", release.Architectures)
	}
	return store, packages
}

func cliPackage(name, epoch, version, iteration, architecture, filename string) *apt.Package {
	pack := apt.NewPackage()
	pack.Name = name
	pack.Epoch = epoch
	pack.Version = version
	pack.Iteration = iteration
	pack.IterationPresent = iteration != ""
	pack.Architecture = architecture
	pack.Category = "utils"
	pack.Maintainer = "Example <example@example.test>"
	pack.IndexFilename = &filename
	return pack
}

func mutationCommandStore(t *testing.T) *storage.MemoryStore {
	t.Helper()
	ctx := context.Background()
	store := storage.NewMemoryStore("mutation-prefix")
	seed := func(codename string, layouts []struct {
		component    string
		architecture string
		packages     []*apt.Package
	}) {
		release := apt.NewRelease(store, apt.ReleaseOptions{Codename: codename, Now: func() time.Time {
			return time.Date(2026, time.August, 4, 20, 0, 0, 0, time.UTC)
		}})
		for _, layout := range layouts {
			manifest := apt.NewManifest(store, apt.ManifestOptions{
				Codename: codename, Component: layout.component, Architecture: layout.architecture,
			})
			manifest.Packages = layout.packages
			if err := manifest.Publish(ctx, nil); err != nil {
				t.Fatal(err)
			}
			release.UpdateManifest(manifest)
		}
		if err := release.Publish(ctx, nil); err != nil {
			t.Fatal(err)
		}
	}
	seed("stable", []struct {
		component    string
		architecture string
		packages     []*apt.Package
	}{
		{component: "main", architecture: "amd64", packages: []*apt.Package{
			cliPackage("alpha", "1", "2.0", "3", "amd64", "pool/stable/alpha-amd64.deb"),
			cliPackage("alpha", "", "3.0", "1", "amd64", "pool/stable/alpha-new.deb"),
		}},
		{component: "main", architecture: "arm64", packages: []*apt.Package{
			cliPackage("alpha", "1", "2.0", "3", "arm64", "pool/stable/alpha-arm64.deb"),
		}},
	})
	seed("testing", []struct {
		component    string
		architecture string
		packages     []*apt.Package
	}{
		{component: "contrib", architecture: "amd64", packages: []*apt.Package{
			cliPackage("destination", "", "1.0", "1", "amd64", "pool/testing/destination.deb"),
		}},
	})
	for _, key := range []string{
		"pool/stable/alpha-amd64.deb", "pool/stable/alpha-new.deb",
		"pool/stable/alpha-arm64.deb", "pool/testing/destination.deb",
	} {
		if err := store.Put(ctx, key, strings.NewReader(key), storage.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func maintenanceCommandStore(t *testing.T) *storage.MemoryStore {
	t.Helper()
	ctx := context.Background()
	store := storage.NewMemoryStore("maintenance-prefix")
	manifest := apt.NewManifest(store, apt.ManifestOptions{Codename: "stable", Component: "main", Architecture: "amd64"})
	manifest.Packages = []*apt.Package{
		cliPackage("present", "", "1.0", "1", "amd64", "pool/stable/present.deb"),
		cliPackage("missing", "", "2.0", "1", "amd64", "pool/stable/missing.deb"),
	}
	if err := manifest.Publish(ctx, nil); err != nil {
		t.Fatal(err)
	}
	release := apt.NewRelease(store, apt.ReleaseOptions{Codename: "stable", Now: func() time.Time {
		return time.Date(2026, time.August, 4, 21, 30, 0, 0, time.UTC)
	}})
	release.UpdateManifest(manifest)
	if err := release.Publish(ctx, nil); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"pool/stable/present.deb":                            "present",
		"pool/stable/unreferenced.deb":                       "garbage",
		"pool/stable-old/unreferenced.deb":                   "other codename",
		"dists/stable/main/binary-amd64/by-hash/SHA256/keep": "immutable",
	} {
		if err := store.Put(ctx, key, strings.NewReader(value), storage.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func cliObjectKeys(objects []storage.ObjectInfo) []string {
	keys := make([]string, 0, len(objects))
	for _, object := range objects {
		keys = append(keys, object.Key)
	}
	return keys
}

type cliLockRecordingStore struct {
	storage.Store
	putKeys []string
}

func (s *cliLockRecordingStore) Put(ctx context.Context, key string, body io.ReadSeeker, options storage.PutOptions) error {
	s.putKeys = append(s.putKeys, key)
	return s.Store.Put(ctx, key, body, options)
}

func (s *cliLockRecordingStore) DeleteIfMatch(ctx context.Context, key, etag string) error {
	conditional, ok := s.Store.(storage.ConditionalDeleter)
	if !ok {
		return errors.New("wrapped store lacks conditional delete")
	}
	return conditional.DeleteIfMatch(ctx, key, etag)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
