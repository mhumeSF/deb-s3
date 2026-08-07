package signing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/mhumesf/deb-s3-go/internal/apt"
)

type GPGSigner struct {
	Provider     string
	Keys         []string
	ExtraOptions []string
	HomeDir      string
	Env          []string
}

func (s GPGSigner) Sign(ctx context.Context, release []byte) (apt.SignatureArtifacts, error) {
	provider := s.Provider
	if provider == "" {
		provider = "gpg"
	}
	directory, err := os.MkdirTemp("", "deb-s3-signing-")
	if err != nil {
		return apt.SignatureArtifacts{}, fmt.Errorf("create signing directory: %w", err)
	}
	defer os.RemoveAll(directory)
	releasePath := filepath.Join(directory, "Release")
	inReleasePath := filepath.Join(directory, "InRelease")
	detachedPath := filepath.Join(directory, "Release.gpg")
	if err := os.WriteFile(releasePath, release, 0o600); err != nil {
		return apt.SignatureArtifacts{}, fmt.Errorf("write Release for signing: %w", err)
	}

	common := []string{"--batch", "--yes", "--armor"}
	for _, key := range s.Keys {
		if strings.TrimSpace(key) == "" {
			return apt.SignatureArtifacts{}, errors.New("GPG signing key cannot be empty")
		}
		common = append(common, "--local-user", key)
	}
	common = append(common, s.ExtraOptions...)
	common = append(common, "--digest-algo", "SHA256")
	clearArgs := append(append([]string{}, common...), "--output", inReleasePath, "--clearsign", releasePath)
	if err := s.run(ctx, provider, clearArgs...); err != nil {
		return apt.SignatureArtifacts{}, fmt.Errorf("create InRelease: %w", err)
	}
	detachedArgs := append(append([]string{}, common...), "--output", detachedPath, "--detach-sign", releasePath)
	if err := s.run(ctx, provider, detachedArgs...); err != nil {
		return apt.SignatureArtifacts{}, fmt.Errorf("create Release.gpg: %w", err)
	}
	if err := requireFile(inReleasePath, "InRelease"); err != nil {
		return apt.SignatureArtifacts{}, err
	}
	if err := requireFile(detachedPath, "Release.gpg"); err != nil {
		return apt.SignatureArtifacts{}, err
	}
	if err := s.run(ctx, provider, "--batch", "--verify", inReleasePath); err != nil {
		return apt.SignatureArtifacts{}, fmt.Errorf("verify InRelease: %w", err)
	}
	if err := s.run(ctx, provider, "--batch", "--verify", detachedPath, releasePath); err != nil {
		return apt.SignatureArtifacts{}, fmt.Errorf("verify Release.gpg: %w", err)
	}
	inRelease, err := os.ReadFile(inReleasePath)
	if err != nil {
		return apt.SignatureArtifacts{}, fmt.Errorf("read InRelease: %w", err)
	}
	detached, err := os.ReadFile(detachedPath)
	if err != nil {
		return apt.SignatureArtifacts{}, fmt.Errorf("read Release.gpg: %w", err)
	}
	return apt.SignatureArtifacts{InRelease: inRelease, ReleaseGPG: detached}, nil
}

func (s GPGSigner) run(ctx context.Context, provider string, args ...string) error {
	command := exec.CommandContext(ctx, provider, args...)
	command.Env = append(os.Environ(), s.Env...)
	if s.HomeDir != "" {
		command.Env = append(command.Env, "GNUPGHOME="+s.HomeDir)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, message)
	}
	return nil
}

func requireFile(filename, name string) error {
	info, err := os.Stat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("GPG did not create %s", name)
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", name, err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("GPG created an empty %s", name)
	}
	return nil
}

// ParseOptions tokenizes --gpg-options without invoking a shell. It supports
// whitespace, single and double quotes, and backslash escapes, but performs no
// variable, command, glob, or tilde expansion.
func ParseOptions(input string) ([]string, error) {
	var options []string
	var current strings.Builder
	quote := rune(0)
	escaped := false
	haveToken := false
	flush := func() {
		if haveToken {
			options = append(options, current.String())
			current.Reset()
			haveToken = false
		}
	}
	for _, character := range input {
		if escaped {
			current.WriteRune(character)
			escaped = false
			haveToken = true
			continue
		}
		if character == '\\' && quote != '\'' {
			escaped = true
			haveToken = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			} else {
				current.WriteRune(character)
			}
			haveToken = true
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			haveToken = true
			continue
		}
		if unicode.IsSpace(character) {
			flush()
			continue
		}
		current.WriteRune(character)
		haveToken = true
	}
	if escaped {
		return nil, errors.New("unterminated escape in GPG options")
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote in GPG options")
	}
	flush()
	return options, nil
}
