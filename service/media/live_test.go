package media

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Explicit opt-in. This exercises ClamAV and a disposable filesystem only;
// it never opens a database, connects to WhatsApp or sends any message.
func TestLiveClamdSecurity(t *testing.T) {
	if os.Getenv("WHATSQR_TEST_CLAMD") != "1" {
		t.Skip("opt-in live scanner validation")
	}
	ctx := context.Background()
	scanner := Clamd{Socket: "/run/clamav/clamd.ctl"}
	if err := scanner.Scan(ctx, strings.NewReader("ordinary clean test bytes")); err != nil {
		t.Fatal("clean scanner check", err)
	}
	// Harmless standard antivirus test signature, assembled only in memory.
	eicar := "X5O!P%@AP[4\\PZX54(P^)7CC)7}$" + "EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*"
	if err := scanner.Scan(ctx, strings.NewReader(eicar)); !errors.Is(err, ErrUnsafe) {
		t.Fatal("EICAR was not rejected", err)
	}
	s, err := New(filepath.Join(t.TempDir(), "private"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind, name, mime string
		data             []byte
		safe             bool
	}{
		{"image", "", "image/png", testPNG(), true},
		{"document", "report.pdf", "application/pdf", []byte("%PDF-1.7\n1 0 obj << /Type /Catalog >> endobj\n%%EOF\n"), true},
		{"document", "disguised.pdf", "application/pdf", []byte("<!doctype html><html><body>Harmless MIME mismatch test.</body></html>"), false},
		{"document", "note.txt", "text/plain", []byte(eicar), false},
		{"image", "", "image/png", []byte("<html><script>bad</script></html>"), false},
	} {
		record := testRecord(tc.data, tc.kind)
		record.Name, record.MIME = tc.name, tc.mime
		id, err := s.Put("test-account", tc.kind+tc.name+string(tc.data[:2]), "test-sender", record)
		if err != nil {
			t.Fatal(err)
		}
		f, _, _, err := s.Open(ctx, "test-account", id, func(_ context.Context, _ Record, out *LimitedFile) error { _, err := out.Write(tc.data); return err })
		if tc.safe {
			if err != nil {
				t.Fatal("clean attachment blocked", tc.kind, err)
			}
			if _, err = io.Copy(io.Discard, f); err != nil {
				t.Fatal(err)
			}
			f.Close()
		} else if err == nil {
			f.Close()
			t.Fatal("unsafe attachment served")
		}
	}
	t.Log("Real ClamAV: clean PNG/PDF allowed; EICAR and disguised HTML blocked; temporary filesystem only.")
}
