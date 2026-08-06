package apt

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"time"

	"github.com/mhumesf/deb-s3/internal/storage"
)

type ManifestArtifact struct {
	Path        string
	ContentType string
	Data        []byte
	Hashes      FileHashes
}

func (a ManifestArtifact) ByHashPath() string {
	return path.Join(path.Dir(a.Path), "by-hash", "SHA256", a.Hashes.SHA256)
}

type ManifestArtifacts struct {
	Packages     ManifestArtifact
	PackagesGzip ManifestArtifact
}

func (m *Manifest) BuildArtifacts() (ManifestArtifacts, error) {
	manifest, err := m.Render()
	if err != nil {
		return ManifestArtifacts{}, err
	}
	plain := []byte(manifest)

	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	writer.Header.ModTime = time.Unix(0, 0).UTC()
	writer.Header.OS = 3
	if _, err := writer.Write(plain); err != nil {
		return ManifestArtifacts{}, fmt.Errorf("compress Packages manifest: %w", err)
	}
	if err := writer.Close(); err != nil {
		return ManifestArtifacts{}, fmt.Errorf("finish Packages manifest compression: %w", err)
	}

	packagesPath := m.packagesPath()
	return ManifestArtifacts{
		Packages: ManifestArtifact{
			Path:        packagesPath,
			ContentType: "text/plain; charset=utf-8",
			Data:        plain,
			Hashes:      hashManifest(plain),
		},
		PackagesGzip: ManifestArtifact{
			Path:        packagesPath + ".gz",
			ContentType: "application/x-gzip; charset=binary",
			Data:        compressed.Bytes(),
			Hashes:      hashManifest(compressed.Bytes()),
		},
	}, nil
}

func (m *Manifest) Publish(ctx context.Context, onTransfer func(string)) error {
	if m.store == nil {
		return errors.New("cannot publish manifest without an object store")
	}
	if err := m.PublishPackages(ctx, onTransfer); err != nil {
		return err
	}
	artifacts, err := m.BuildArtifacts()
	if err != nil {
		return err
	}
	if err := m.PublishByHash(ctx, artifacts, onTransfer); err != nil {
		return err
	}
	if err := m.PublishCanonical(ctx, artifacts, onTransfer); err != nil {
		return err
	}
	m.RecordArtifacts(artifacts)
	return nil
}

func (m *Manifest) PublishPackages(ctx context.Context, onTransfer func(string)) error {
	if m.store == nil {
		return errors.New("cannot publish packages without an object store")
	}
	if m.SkipPackageUpload {
		return nil
	}
	for _, pack := range m.PendingUploads {
		if err := m.publishPackage(ctx, pack, onTransfer); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manifest) PublishByHash(ctx context.Context, artifacts ManifestArtifacts, onTransfer func(string)) error {
	if m.store == nil {
		return errors.New("cannot publish by-hash manifests without an object store")
	}
	if !m.ByHash {
		return nil
	}
	for _, artifact := range artifactList(artifacts) {
		filename := artifact.ByHashPath()
		if onTransfer != nil {
			onTransfer(filename)
		}
		if err := m.store.Put(ctx, filename, bytes.NewReader(artifact.Data), storage.PutOptions{
			ContentType:  artifact.ContentType,
			CacheControl: m.CacheControl,
			CreateOnly:   true,
		}); err != nil {
			return fmt.Errorf("store immutable manifest %q: %w", filename, err)
		}
	}
	return nil
}

func (m *Manifest) PublishCanonical(ctx context.Context, artifacts ManifestArtifacts, onTransfer func(string)) error {
	if m.store == nil {
		return errors.New("cannot publish canonical manifests without an object store")
	}
	for _, artifact := range artifactList(artifacts) {
		if onTransfer != nil {
			onTransfer(artifact.Path)
		}
		if err := m.store.Put(ctx, artifact.Path, bytes.NewReader(artifact.Data), storage.PutOptions{
			ContentType:  artifact.ContentType,
			CacheControl: m.CacheControl,
		}); err != nil {
			return fmt.Errorf("store manifest %q: %w", artifact.Path, err)
		}
	}
	return nil
}

func (m *Manifest) RecordArtifacts(artifacts ManifestArtifacts) {
	if m.Files == nil {
		m.Files = make(map[string]FileHashes)
	}
	m.Files[m.releasePackagesPath()] = artifacts.Packages.Hashes
	m.Files[m.releasePackagesPath()+".gz"] = artifacts.PackagesGzip.Hashes
}

func artifactList(artifacts ManifestArtifacts) []ManifestArtifact {
	return []ManifestArtifact{artifacts.Packages, artifacts.PackagesGzip}
}

func (m *Manifest) publishPackage(ctx context.Context, pack *Package, onTransfer func(string)) error {
	if pack.Filename == "" {
		return fmt.Errorf("package %q has no source filename", pack.Name)
	}
	repositoryFilename, err := pack.RepositoryFilename(m.Codename)
	if err != nil {
		return err
	}
	file, err := os.Open(pack.Filename)
	if err != nil {
		return fmt.Errorf("open package %q: %w", pack.Filename, err)
	}
	if onTransfer != nil {
		onTransfer(repositoryFilename)
	}
	putErr := m.store.Put(ctx, repositoryFilename, file, storage.PutOptions{
		ContentType:  "application/octet-stream; charset=binary",
		CacheControl: m.CacheControl,
		FailIfExists: m.FailIfExists,
	})
	closeErr := file.Close()
	if putErr != nil {
		return fmt.Errorf("store package %q: %w", repositoryFilename, putErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close package %q: %w", pack.Filename, closeErr)
	}
	return nil
}

func hashManifest(data []byte) FileHashes {
	md5Hash := md5.Sum(data)
	sha1Hash := sha1.Sum(data)
	sha256Hash := sha256.Sum256(data)
	return FileHashes{
		Size:   int64(len(data)),
		MD5:    hex.EncodeToString(md5Hash[:]),
		SHA1:   hex.EncodeToString(sha1Hash[:]),
		SHA256: hex.EncodeToString(sha256Hash[:]),
	}
}
