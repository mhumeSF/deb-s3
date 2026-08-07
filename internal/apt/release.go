package apt

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mhumesf/deb-s3-go/internal/storage"
)

var releaseArchitectures = []string{"amd64", "i386", "armhf", "arm64"}

type ReleaseOptions struct {
	Codename     string
	Origin       *string
	Suite        *string
	CacheControl string
	ByHash       *bool
	Signer       ReleaseSigner
	Now          func() time.Time
}

type SignatureArtifacts struct {
	InRelease  []byte
	ReleaseGPG []byte
}

type ReleaseSigner interface {
	Sign(context.Context, []byte) (SignatureArtifacts, error)
}

type Release struct {
	store storage.Store

	Codename      string
	Origin        *string
	Suite         *string
	Architectures []string
	Components    []string
	CacheControl  string
	ByHash        bool
	Signer        ReleaseSigner
	Files         map[string]FileHashes
	Now           func() time.Time
}

func NewRelease(store storage.Store, options ReleaseOptions) *Release {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	release := &Release{
		store:        store,
		Codename:     options.Codename,
		Origin:       cloneStringPointer(options.Origin),
		Suite:        cloneStringPointer(options.Suite),
		CacheControl: options.CacheControl,
		Signer:       options.Signer,
		Files:        make(map[string]FileHashes),
		Now:          now,
	}
	if options.ByHash != nil {
		release.ByHash = *options.ByHash
	}
	return release
}

func ParseRelease(input string) (*Release, error) {
	release := NewRelease(nil, ReleaseOptions{})
	release.Codename, _ = releaseField(input, "Codename")
	if value, ok := releaseField(input, "Origin"); ok {
		release.Origin = &value
	}
	if value, ok := releaseField(input, "Suite"); ok {
		release.Suite = &value
	}
	if value, ok := releaseField(input, "Acquire-By-Hash"); ok {
		release.ByHash = strings.EqualFold(strings.TrimSpace(value), "yes")
	}
	if value, ok := releaseField(input, "Architectures"); ok {
		release.Architectures = strings.Fields(value)
	}
	if value, ok := releaseField(input, "Components"); ok {
		release.Components = strings.Fields(value)
	}

	for _, line := range strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n") {
		if len(line) == 0 || (line[0] != ' ' && line[0] != '\t') {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 3 {
			continue
		}
		size, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || size < 0 {
			continue
		}
		filename := parts[2]
		hashes := release.Files[filename]
		hashes.Size = size
		switch len(parts[0]) {
		case 32:
			hashes.MD5 = parts[0]
		case 40:
			hashes.SHA1 = parts[0]
		case 64:
			hashes.SHA256 = parts[0]
		default:
			continue
		}
		release.Files[filename] = hashes
	}
	return release, nil
}

func (r *Release) Filename() string {
	return path.Join("dists", r.Codename, "Release")
}

func (r *Release) Render() (string, error) {
	if r.Now == nil {
		return "", errors.New("release clock is not configured")
	}
	var output strings.Builder
	if r.Origin != nil {
		writeField(&output, "Origin", *r.Origin)
	}
	writeField(&output, "Codename", r.Codename)
	writeField(&output, "Date", r.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 UTC"))
	writeField(&output, "Architectures", strings.Join(r.Architectures, " "))
	writeField(&output, "Components", strings.Join(r.Components, " "))
	writeField(&output, "Suite", pointerValue(r.Suite))
	if r.ByHash {
		writeField(&output, "Acquire-By-Hash", "yes")
	}

	filenames := make([]string, 0, len(r.Files))
	for filename := range r.Files {
		filenames = append(filenames, filename)
	}
	sort.Strings(filenames)
	writeReleaseHashes(&output, "MD5Sum", filenames, r.Files, func(hashes FileHashes) string { return hashes.MD5 })
	writeReleaseHashes(&output, "SHA1", filenames, r.Files, func(hashes FileHashes) string { return hashes.SHA1 })
	writeReleaseHashes(&output, "SHA256", filenames, r.Files, func(hashes FileHashes) string { return hashes.SHA256 })
	return output.String(), nil
}

func (r *Release) UpdateManifest(manifest *Manifest) {
	if manifest == nil {
		return
	}
	r.Components = appendUnique(r.Components, manifest.Component)
	r.Architectures = appendUnique(r.Architectures, manifest.Architecture)
	if r.Files == nil {
		r.Files = make(map[string]FileHashes)
	}
	for filename, hashes := range manifest.Files {
		r.Files[filename] = hashes
	}
}

func releaseField(input, name string) (string, bool) {
	prefix := name + ": "
	for _, line := range strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix), true
		}
	}
	return "", false
}

func writeReleaseHashes(output *strings.Builder, heading string, filenames []string, files map[string]FileHashes, hash func(FileHashes) string) {
	output.WriteString(heading)
	output.WriteString(":\n")
	for _, filename := range filenames {
		hashes := files[filename]
		fmt.Fprintf(output, " %s %16d %s\n", hash(hashes), hashes.Size, filename)
	}
}

func appendUnique(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
