package lock

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/deb-s3/deb-s3/internal/storage"
)

var ErrLocked = errors.New("repository is locked")
var ErrNotOwner = errors.New("repository lock is owned by another process")
var ErrConditionalDeleteUnsupported = errors.New("object store does not support ownership-safe conditional delete")

type Owner struct {
	Token      string    `json:"token"`
	User       string    `json:"user"`
	Host       string    `json:"host"`
	PID        int       `json:"pid"`
	AcquiredAt time.Time `json:"acquired_at"`
}

type WaitInfo struct {
	Current Owner
	Attempt int
	Delay   time.Duration
}

type Options struct {
	Owner          Owner
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	CacheControl   string
	Reporter       func(WaitInfo)
	Now            func() time.Time
	ReleaseTimeout time.Duration
}

type Manager struct {
	Store   storage.Store
	Options Options
}

type Handle struct {
	store storage.Store
	path  string
	owner Owner
	etag  string

	mu       sync.Mutex
	released bool
}

func (m Manager) Acquire(ctx context.Context, codename string) (*Handle, error) {
	if m.Store == nil {
		return nil, errors.New("cannot acquire a lock without an object store")
	}
	if _, ok := m.Store.(storage.ConditionalDeleter); !ok {
		return nil, ErrConditionalDeleteUnsupported
	}
	lockPath, err := checkedLockPath(codename)
	if err != nil {
		return nil, err
	}
	options := m.optionsWithDefaults()
	owner := options.Owner
	if owner.Token == "" {
		owner, err = newOwner(options.Now)
		if err != nil {
			return nil, err
		}
	} else if owner.AcquiredAt.IsZero() {
		owner.AcquiredAt = options.Now().UTC()
	}
	body, err := json.Marshal(owner)
	if err != nil {
		return nil, fmt.Errorf("encode lock owner: %w", err)
	}

	for attempt := 1; attempt <= options.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err := m.Store.Put(ctx, lockPath, bytes.NewReader(body), storage.PutOptions{
			ContentType: "text/plain; charset=utf-8", CacheControl: options.CacheControl, CreateOnly: true,
		})
		if err == nil {
			info, err := m.Store.Head(ctx, lockPath)
			if err != nil {
				return nil, fmt.Errorf("read acquired repository lock: %w", err)
			}
			return &Handle{store: m.Store, path: lockPath, owner: owner, etag: info.ETag}, nil
		}
		if !errors.Is(err, storage.ErrConflict) {
			return nil, fmt.Errorf("create repository lock: %w", err)
		}

		current, currentErr := m.Current(ctx, codename)
		if currentErr != nil && !errors.Is(currentErr, storage.ErrNotFound) {
			return nil, fmt.Errorf("read current repository lock: %w", currentErr)
		}
		delay := retryDelay(options.InitialBackoff, options.MaxBackoff, attempt)
		if options.Reporter != nil {
			info := WaitInfo{Attempt: attempt, Delay: delay}
			if current != nil {
				info.Current = *current
			}
			options.Reporter(info)
		}
		if attempt == options.MaxAttempts {
			break
		}
		if currentErr != nil && errors.Is(currentErr, storage.ErrNotFound) {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, fmt.Errorf("%w after %d attempts", ErrLocked, options.MaxAttempts)
}

func (m Manager) Current(ctx context.Context, codename string) (*Owner, error) {
	if m.Store == nil {
		return nil, errors.New("cannot read a lock without an object store")
	}
	lockPath, err := checkedLockPath(codename)
	if err != nil {
		return nil, err
	}
	body, err := m.Store.Get(ctx, lockPath)
	if err != nil {
		return nil, err
	}
	return decodeOwner(body)
}

func (m Manager) WithLock(ctx context.Context, codename string, operation func(context.Context) error) (returnErr error) {
	if operation == nil {
		return errors.New("lock operation cannot be nil")
	}
	handle, err := m.Acquire(ctx, codename)
	if err != nil {
		return err
	}
	options := m.optionsWithDefaults()
	defer func() {
		releaseContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), options.ReleaseTimeout)
		defer cancel()
		returnErr = errors.Join(returnErr, handle.Release(releaseContext))
	}()
	return operation(ctx)
}

func (h *Handle) Owner() Owner { return h.owner }

func (h *Handle) Release(ctx context.Context) error {
	if h == nil || h.store == nil {
		return errors.New("invalid repository lock handle")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.released {
		return nil
	}
	body, err := h.store.Get(ctx, h.path)
	if errors.Is(err, storage.ErrNotFound) {
		h.released = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("read repository lock before release: %w", err)
	}
	current, err := decodeOwner(body)
	if err != nil || current.Token != h.owner.Token {
		return ErrNotOwner
	}
	conditional, ok := h.store.(storage.ConditionalDeleter)
	if !ok {
		return ErrConditionalDeleteUnsupported
	}
	err = conditional.DeleteIfMatch(ctx, h.path, h.etag)
	if errors.Is(err, storage.ErrConflict) {
		return ErrNotOwner
	}
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("delete repository lock: %w", err)
	}
	h.released = true
	return nil
}

func LockPath(codename string) string {
	return path.Join("dists", codename, "lockfile")
}

func checkedLockPath(codename string) (string, error) {
	cleaned := path.Clean(codename)
	if cleaned == "." || path.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("unsafe repository codename %q", codename)
	}
	return LockPath(cleaned), nil
}

func (m Manager) optionsWithDefaults() Options {
	options := m.Options
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = 60
	}
	if options.InitialBackoff <= 0 {
		options.InitialBackoff = 100 * time.Millisecond
	}
	if options.MaxBackoff <= 0 {
		options.MaxBackoff = 10 * time.Second
	}
	if options.MaxBackoff < options.InitialBackoff {
		options.MaxBackoff = options.InitialBackoff
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ReleaseTimeout <= 0 {
		options.ReleaseTimeout = 30 * time.Second
	}
	return options
}

func retryDelay(initial, maximum time.Duration, attempt int) time.Duration {
	delay := initial
	for step := 1; step < attempt && delay < maximum; step++ {
		if delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func newOwner(now func() time.Time) (Owner, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return Owner{}, fmt.Errorf("generate lock owner token: %w", err)
	}
	host, _ := os.Hostname()
	username := os.Getenv("USER")
	if current, err := user.Current(); err == nil && current.Username != "" {
		username = current.Username
	}
	return Owner{
		Token: hex.EncodeToString(random), User: username, Host: host,
		PID: os.Getpid(), AcquiredAt: now().UTC(),
	}, nil
}

func decodeOwner(body []byte) (*Owner, error) {
	var owner Owner
	if err := json.Unmarshal(body, &owner); err == nil && owner.Token != "" {
		return &owner, nil
	}
	legacy := strings.TrimSpace(string(body))
	username, host, found := strings.Cut(legacy, "@")
	if found && username != "" && host != "" {
		return &Owner{Token: legacy, User: username, Host: host}, nil
	}
	return nil, errors.New("repository lock contains invalid owner data")
}
