package webhook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const maxHistoryOutboxBytes = 128 << 20
const maxHistoryEventBytes = 128 << 10

// HistoryOutbox retains imports until Mautic acknowledges them. No tokens,
// media keys or downloaded attachments belong here. Files are private (0600).
type HistoryOutbox struct {
	mu    sync.Mutex
	dir   string
	bytes int64
	files []string
	sizes map[string]int64
}

func NewHistoryOutbox(dir string) (*HistoryOutbox, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("history outbox must be a private directory")
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	h := &HistoryOutbox{dir: dir, sizes: make(map[string]int64)}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if len(name) != 69 || filepath.Ext(name) != ".json" {
			continue
		}
		if _, err = hex.DecodeString(name[:64]); err != nil {
			return nil, errors.New("invalid history filename")
		}
		info, err = entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxHistoryEventBytes {
			return nil, errors.New("unsafe history event file")
		}
		h.files = append(h.files, name)
		h.sizes[name] = info.Size()
		h.bytes += info.Size()
	}
	if h.bytes > maxHistoryOutboxBytes {
		return nil, errors.New("history outbox size limit exceeded")
	}
	sort.Strings(h.files)
	return h, nil
}

func historyFilename(ev *event) string {
	hash := sha256.Sum256([]byte(ev.sessionID + "\x00" + ev.id))
	return hex.EncodeToString(hash[:]) + ".json"
}

func (h *HistoryOutbox) enqueue(ev *event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	name := historyFilename(ev)
	if _, exists := h.sizes[name]; exists {
		return nil
	}
	if len(ev.body) > maxHistoryEventBytes || h.bytes+int64(len(ev.body)) > maxHistoryOutboxBytes {
		return errors.New("history outbox size limit exceeded")
	}
	file, err := os.CreateTemp(h.dir, ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(ev.body); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(file.Name(), filepath.Join(h.dir, name)); err != nil {
		return err
	}
	if err = syncHistoryDirectory(h.dir); err != nil {
		return err
	}
	h.files = append(h.files, name)
	h.sizes[name] = int64(len(ev.body))
	h.bytes += int64(len(ev.body))
	return nil
}

func (h *HistoryOutbox) peek() (*event, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.files) == 0 {
		return nil, nil
	}
	filename := filepath.Join(h.dir, h.files[0])
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxHistoryEventBytes {
		return nil, errors.New("unsafe history event file")
	}
	raw, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var body payload
	if err = json.Unmarshal(raw, &body); err != nil {
		return nil, errors.New("invalid history event")
	}
	if body.Message == nil || !body.Message.Historical || body.ID == "" || body.SessionID == "" {
		return nil, errors.New("invalid history payload")
	}
	ev := &event{id: body.ID, kind: body.Type, sessionID: body.SessionID, body: raw}
	if historyFilename(ev) != h.files[0] {
		return nil, errors.New("history filename does not match payload")
	}
	return ev, nil
}

func (h *HistoryOutbox) complete(ev *event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	name := historyFilename(ev)
	if len(h.files) == 0 || h.files[0] != name {
		return errors.New("history acknowledgement order mismatch")
	}
	if err := os.Remove(filepath.Join(h.dir, name)); err != nil {
		return err
	}
	h.bytes -= h.sizes[name]
	delete(h.sizes, name)
	h.files = h.files[1:]
	return syncHistoryDirectory(h.dir)
}

func syncHistoryDirectory(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
