package signing

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestParseOptions(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "empty"},
		{name: "words", input: "--pinentry-mode loopback", want: []string{"--pinentry-mode", "loopback"}},
		{name: "quotes and escapes", input: `--comment "release signer" '--literal=$HOME' escaped\ value ""`, want: []string{"--comment", "release signer", "--literal=$HOME", "escaped value", ""}},
		{name: "no command expansion", input: `'$(touch /tmp/not-executed)'`, want: []string{"$(touch /tmp/not-executed)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseOptions(tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseOptions(%q) = %#v, want %#v", tt.input, got, tt.want)
			}
		})
	}
	for _, input := range []string{`"unterminated`, `trailing\`} {
		if _, err := ParseOptions(input); err == nil {
			t.Fatalf("ParseOptions(%q) succeeded", input)
		}
	}
}

func TestGPGSignerUsesArgumentVectorAndVerifiesArtifacts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX executable script")
	}
	directory := t.TempDir()
	logPath := filepath.Join(directory, "arguments.log")
	marker := filepath.Join(directory, "shell-expanded")
	provider := writeExecutable(t, directory, "fake-gpg", `#!/bin/sh
output=
action=
while [ "$#" -gt 0 ]; do
  printf '%s\n' "$1" >> "$FAKE_GPG_LOG"
  case "$1" in
    --output) shift; output=$1 ;;
    --clearsign) action=clear ;;
    --detach-sign) action=detached ;;
    --verify) exit 0 ;;
  esac
  shift
done
if [ -n "$output" ]; then
  printf '%s signature\n' "$action" > "$output"
fi
`)
	signer := GPGSigner{
		Provider: provider,
		Keys:     []string{"first-key", "second-key"},
		ExtraOptions: []string{
			"--pinentry-mode", "loopback", "$(touch " + marker + ")",
		},
		Env: []string{"FAKE_GPG_LOG=" + logPath},
	}
	artifacts, err := signer.Sign(context.Background(), []byte("Codename: stable\n"))
	if err != nil {
		t.Fatal(err)
	}
	if string(artifacts.InRelease) != "clear signature\n" || string(artifacts.ReleaseGPG) != "detached signature\n" {
		t.Fatalf("artifacts = %#v", artifacts)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("extra option was interpreted by a shell: %v", err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	arguments := strings.Split(strings.TrimSpace(string(logData)), "\n")
	for _, required := range []string{"--local-user", "first-key", "second-key", "--digest-algo", "SHA256", "--clearsign", "--detach-sign", "--verify", "$(touch " + marker + ")"} {
		if !contains(arguments, required) {
			t.Errorf("argument log lacks %q: %#v", required, arguments)
		}
	}
}

func TestGPGSignerReportsProviderAndArtifactFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX executable script")
	}
	directory := t.TempDir()
	failing := writeExecutable(t, directory, "failing-gpg", "#!/bin/sh\necho key unavailable >&2\nexit 7\n")
	if _, err := (GPGSigner{Provider: failing}).Sign(context.Background(), []byte("Release")); err == nil || !strings.Contains(err.Error(), "key unavailable") {
		t.Fatalf("provider failure = %v", err)
	}
	missing := writeExecutable(t, directory, "missing-output-gpg", "#!/bin/sh\nexit 0\n")
	if _, err := (GPGSigner{Provider: missing}).Sign(context.Background(), []byte("Release")); err == nil || !strings.Contains(err.Error(), "did not create InRelease") {
		t.Fatalf("missing artifact failure = %v", err)
	}
}

func TestGPGSignerWithIsolatedRealKeyring(t *testing.T) {
	gpg, err := exec.LookPath("gpg")
	if err != nil {
		t.Skip("gpg is not installed")
	}
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	generate := exec.Command(gpg,
		"--batch", "--homedir", home, "--passphrase", "",
		"--quick-generate-key", "deb-s3 test <deb-s3@example.invalid>", "rsa2048", "sign", "0",
	)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("generate isolated GPG key: %v: %s", err, output)
	}
	artifacts, err := (GPGSigner{Provider: gpg, HomeDir: home}).Sign(context.Background(), []byte("Codename: stable\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts.InRelease) == 0 || len(artifacts.ReleaseGPG) == 0 {
		t.Fatal("real GPG returned empty artifacts")
	}
}

func writeExecutable(t *testing.T, directory, name, contents string) string {
	t.Helper()
	filename := filepath.Join(directory, name)
	if err := os.WriteFile(filename, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	return filename
}

func contains(values []string, value string) bool {
	for _, existing := range values {
		if existing == value {
			return true
		}
	}
	return false
}
