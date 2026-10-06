// Package media keeps received attachment references and downloaded bytes outside
// the web root. The database and signed webhook never receive encryption keys.
package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const MaxBytes int64 = 32 << 20
const maxDescriptorBytes = 32 << 10
const maxStoreBytes int64 = 2 << 30
const maxRecords = 50000

var ErrUnavailable = errors.New("media unavailable")
var ErrTooLarge = errors.New("attachment exceeds 32 MiB")
var ErrFull = errors.New("private media storage is full")

type Record struct {
	Type       string `json:"type"`
	Name       string `json:"name"`
	MIME       string `json:"mime"`
	Size       uint64 `json:"size"`
	Descriptor []byte `json:"descriptor"`
	SHA256     string `json:"sha256"`
}

type pending struct {
	done chan struct{}
	err  error
}
type Store struct {
	root    string
	mu      sync.Mutex
	bytes   int64
	records int
	pending map[string]*pending
	slots   chan struct{}
	scanner Scanner
}

func New(root string, scanners ...Scanner) (*Store, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnavailable
	}
	if err = os.Chmod(root, 0700); err != nil {
		return nil, err
	}
	s := &Store{root: root, pending: map[string]*pending{}, slots: make(chan struct{}, 2), scanner: Clamd{Socket: "/run/clamav/clamd.ctl"}}
	if len(scanners) > 0 {
		s.scanner = scanners[0]
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		// Count crash leftovers as well: they must not bypass the disk quota.
		info, err := entry.Info()
		if err != nil {
			return err
		}
		s.bytes += info.Size()
		if strings.HasSuffix(path, ".json") {
			s.records++
		}
		return nil
	})
	return s, err
}

func ValidID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func validType(t string) bool {
	return t == "image" || t == "video" || t == "audio" || t == "document" || t == "sticker"
}
func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func (s *Store) paths(account, id string) (string, string, error) {
	if account == "" || !ValidID(id) {
		return "", "", ErrUnavailable
	}
	dir := filepath.Join(s.root, digest(account))
	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", "", ErrUnavailable
		}
	} else if !os.IsNotExist(err) {
		return "", "", ErrUnavailable
	}
	return filepath.Join(dir, id+".json"), filepath.Join(dir, id+".bin"), nil
}

func (s *Store) Put(account, messageID, sender string, record Record) (string, error) {
	if account == "" || messageID == "" || sender == "" || !validType(record.Type) || len(record.Descriptor) == 0 || !ValidID(record.SHA256) || len(record.MIME) > 256 || len(record.Name) > 640 {
		return "", ErrUnavailable
	}
	if record.Size == 0 || record.Size > uint64(MaxBytes) {
		return "", ErrTooLarge
	}
	id := digest(sender + "\x00" + messageID)
	meta, _, err := s.paths(account, id)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(record)
	if err != nil || len(data) > maxDescriptorBytes {
		return "", ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Redelivery must not overwrite the original descriptor or duplicate files.
	if info, statErr := os.Lstat(meta); statErr == nil {
		if !info.Mode().IsRegular() {
			return "", ErrUnavailable
		}
		return id, nil
	} else if !os.IsNotExist(statErr) {
		return "", statErr
	}
	if s.records >= maxRecords || s.bytes+int64(len(data)) > maxStoreBytes {
		return "", ErrFull
	}
	if err = os.MkdirAll(filepath.Dir(meta), 0700); err != nil {
		return "", err
	}
	if err = atomicWrite(meta, data); err != nil {
		return "", err
	}
	s.records++
	s.bytes += int64(len(data))
	return id, nil
}

func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".reference-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (s *Store) Read(account, id string) (Record, error) {
	meta, _, err := s.paths(account, id)
	if err != nil {
		return Record{}, err
	}
	f, err := openRegular(meta)
	if err != nil {
		return Record{}, ErrUnavailable
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxDescriptorBytes+1))
	if err != nil || len(data) > maxDescriptorBytes {
		return Record{}, ErrUnavailable
	}
	var record Record
	if json.Unmarshal(data, &record) != nil || !validType(record.Type) || record.Size == 0 || record.Size > uint64(MaxBytes) {
		return Record{}, ErrUnavailable
	}
	// Compatibility with previously captured private Whatsmeow descriptors.
	if record.SHA256 == "" {
		var old struct {
			SHA []byte `json:"sha"`
		}
		if json.Unmarshal(record.Descriptor, &old) == nil && len(old.SHA) == 32 {
			record.SHA256 = hex.EncodeToString(old.SHA)
		}
	}
	if !ValidID(record.SHA256) {
		return Record{}, ErrUnavailable
	}
	return record, nil
}

func openRegular(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrUnavailable
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) {
		f.Close()
		return nil, ErrUnavailable
	}
	return f, nil
}

// Open coalesces requests for one file and bounds network work to two downloads.
// The caller owns the returned file. Downloads stream to disk with a hard bound.
func (s *Store) Open(ctx context.Context, account, id string, fetch func(context.Context, Record, *LimitedFile) error) (*os.File, Record, string, error) {
	record, err := s.Read(account, id)
	if err != nil {
		return nil, Record{}, "", err
	}
	_, path, err := s.paths(account, id)
	if err != nil {
		return nil, record, "", err
	}
	if f, err := openRegular(path); err == nil {
		mime, err := s.validate(ctx, f, record)
		if err == nil {
			return f, record, mime, nil
		}
		f.Close()
		return nil, record, "", err
	}
	s.mu.Lock()
	key := account + "\x00" + id
	if p := s.pending[key]; p != nil {
		s.mu.Unlock()
		select {
		case <-p.done:
			if p.err != nil {
				return nil, record, "", p.err
			}
		case <-ctx.Done():
			return nil, record, "", ctx.Err()
		}
		f, err := openRegular(path)
		if err != nil {
			return nil, record, "", err
		}
		mime, err := s.validate(ctx, f, record)
		if err != nil {
			f.Close()
			return nil, record, "", err
		}
		return f, record, mime, nil
	}
	if len(s.pending) >= 32 {
		s.mu.Unlock()
		return nil, record, "", ErrUnavailable
	}
	p := &pending{done: make(chan struct{})}
	s.pending[key] = p
	s.mu.Unlock()
	err = s.download(ctx, path, record, fetch)
	s.mu.Lock()
	p.err = err
	delete(s.pending, key)
	close(p.done)
	s.mu.Unlock()
	if err != nil {
		return nil, record, "", err
	}
	f, err := openRegular(path)
	if err != nil {
		return nil, record, "", err
	}
	mime, err := s.validate(ctx, f, record)
	if err != nil {
		f.Close()
		return nil, record, "", err
	}
	return f, record, mime, nil
}

func (s *Store) download(ctx context.Context, path string, record Record, fetch func(context.Context, Record, *LimitedFile) error) error {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	// Reserve space before downloading, including simultaneous downloads.
	s.mu.Lock()
	if s.bytes+MaxBytes+32 > maxStoreBytes {
		s.mu.Unlock()
		return ErrFull
	}
	s.bytes += MaxBytes + 32
	s.mu.Unlock()
	committed := int64(0)
	defer func() { s.mu.Lock(); s.bytes += committed - MaxBytes - 32; s.mu.Unlock() }()
	f, err := os.CreateTemp(filepath.Dir(path), ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = fetch(ctx, record, &LimitedFile{File: f, limit: MaxBytes + 32}); err != nil {
		return err
	}
	if _, err = s.check(ctx, f, record); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	committed = info.Size()
	return nil
}

// Wrap *os.File so the Whatsmeow downloader cannot preallocate an unbounded
// Content-Length, nor write more than the encrypted attachment bound.
type LimitedFile struct {
	*os.File
	limit int64
}

func (f *LimitedFile) Write(p []byte) (int, error) {
	pos, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if int64(len(p)) > f.limit-pos {
		return 0, ErrTooLarge
	}
	return f.File.Write(p)
}
func (f *LimitedFile) ReadFrom(r io.Reader) (int64, error) {
	// Do not inherit os.File.ReadFrom: io.Copy would bypass the bound in Write.
	return io.Copy(struct{ io.Writer }{f}, r)
}
func (f *LimitedFile) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || int64(len(p)) > f.limit-off {
		return 0, ErrTooLarge
	}
	return f.File.WriteAt(p, off)
}
func (f *LimitedFile) Truncate(size int64) error {
	if size < 0 || size > f.limit {
		return ErrTooLarge
	}
	return f.File.Truncate(size)
}

func (s *Store) String() string {
	return fmt.Sprintf("private attachment store (%d MiB per attachment)", MaxBytes>>20)
}

// DownloadTimeout bounds retries in the Whatsmeow downloader, including network IO.
const DownloadTimeout = 25 * time.Second
