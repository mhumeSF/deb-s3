package config

import (
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	cfg := New()
	if cfg.Codename != "stable" {
		t.Fatalf("Codename = %q, want stable", cfg.Codename)
	}
	if cfg.Component != "main" {
		t.Fatalf("Component = %q, want main", cfg.Component)
	}
	if cfg.S3Region != "" {
		t.Fatalf("S3Region = %q, want empty so the AWS configuration chain resolves it", cfg.S3Region)
	}
	if cfg.Visibility != "public" {
		t.Fatalf("Visibility = %q, want public", cfg.Visibility)
	}
	if cfg.GPGProvider != "gpg" {
		t.Fatalf("GPGProvider = %q, want gpg", cfg.GPGProvider)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		change  func(*Config)
		wantErr string
	}{
		{
			name:    "bucket is required",
			change:  func(*Config) {},
			wantErr: "--bucket",
		},
		{
			name: "access key requires secret",
			change: func(c *Config) {
				c.Bucket = "repo"
				c.AccessKeyID = "key"
			},
			wantErr: "must specify the other",
		},
		{
			name: "secret requires access key",
			change: func(c *Config) {
				c.Bucket = "repo"
				c.SecretAccessKey = "secret"
			},
			wantErr: "must specify the other",
		},
		{
			name: "invalid visibility",
			change: func(c *Config) {
				c.Bucket = "repo"
				c.Visibility = "world"
			},
			wantErr: "invalid visibility",
		},
		{
			name: "valid explicit credentials",
			change: func(c *Config) {
				c.Bucket = "repo"
				c.AccessKeyID = "key"
				c.SecretAccessKey = "secret"
				c.SessionToken = "token"
			},
		},
		{
			name: "no ACL visibility",
			change: func(c *Config) {
				c.Bucket = "repo"
				c.Visibility = "nil"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := New()
			tt.change(&cfg)
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateArchitecture(t *testing.T) {
	for _, architecture := range []string{"", "all", "amd64", "arm64", "linux-any"} {
		if err := ValidateArchitecture(architecture); err != nil {
			t.Errorf("ValidateArchitecture(%q) = %v", architecture, err)
		}
	}
	for _, architecture := range []string{"AMD64", "../amd64", "arm_64", "amd64/other"} {
		if err := ValidateArchitecture(architecture); err == nil {
			t.Errorf("ValidateArchitecture(%q) succeeded, want error", architecture)
		}
	}
}
