package media

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type rejectingScanner struct{ err error }

func (s rejectingScanner) Scan(context.Context, io.Reader) error { return s.err }

func testOffice(parts map[string][]byte) []byte {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, data := range parts {
		f, _ := w.Create(name)
		f.Write(data)
	}
	w.Close()
	return b.Bytes()
}

func TestAttachmentContentSecurity(t *testing.T) {
	goodPNG := testPNG()
	bomb := append([]byte(nil), goodPNG...)
	binary.BigEndian.PutUint32(bomb[16:20], 100000)
	binary.BigEndian.PutUint32(bomb[20:24], 100000)
	binary.BigEndian.PutUint32(bomb[29:33], crc32.ChecksumIEEE(bomb[12:29]))
	goodPDF := []byte("%PDF-1.7\n1 0 obj << /Type /Catalog >> endobj\n%%EOF\n")
	goodOffice := map[string][]byte{"[Content_Types].xml": []byte(`<Types/>`), "word/document.xml": []byte(`<document/>`)}
	macroOffice := map[string][]byte{"[Content_Types].xml": []byte(`<Types/>`), "word/document.xml": []byte(`<document/>`), "word/vbaProject.bin": []byte("macro")}
	cases := []struct {
		name, kind, filename, mime string
		data                       []byte
		safe                       bool
	}{
		{"png", "image", "", "image/png", goodPNG, true},
		{"wrong MIME", "image", "", "image/jpeg", goodPNG, false},
		{"wrong extension", "image", "photo.exe", "image/png", goodPNG, false},
		{"double extension", "document", "report.pdf.exe", "application/pdf", goodPDF, false},
		{"image header only", "image", "", "image/png", goodPNG[:33], false},
		{"image dimension bomb", "image", "", "image/png", bomb, false},
		{"HTML as image", "image", "", "image/png", []byte("<html>unsafe</html>"), false},
		{"SVG", "image", "photo.svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), false},
		{"PDF", "document", "report.pdf", "application/pdf", goodPDF, true},
		{"PDF javascript", "document", "report.pdf", "application/pdf", []byte("%PDF-1.7\n/Java#53cript (script)\n%%EOF"), false},
		{"PDF encrypted", "document", "report.pdf", "application/pdf", []byte("%PDF-1.7\n/Encrypt 1 0 R\n%%EOF"), false},
		{"PDF truncated", "document", "report.pdf", "application/pdf", []byte("%PDF-1.7 broken"), false},
		{"executable", "document", "file.pdf", "application/pdf", []byte("MZ\x00\x00binary executable"), false},
		{"text", "document", "note.txt", "text/plain", []byte("An ordinary support note.\n"), true},
		{"script as text", "document", "note.txt", "text/plain", []byte("#!/bin/sh\necho unsafe"), false},
		{"CSV formula", "document", "table.csv", "text/plain", []byte("name,value\nuser,=WEBSERVICE(1)"), false},
		{"office", "document", "report.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", testOffice(goodOffice), true},
		{"office macro", "document", "report.docx", "application/octet-stream", testOffice(macroOffice), false},
		{"office traversal", "document", "report.docx", "application/octet-stream", testOffice(map[string][]byte{"../secret": []byte("x")}), false},
		{"office external", "document", "report.docx", "application/octet-stream", testOffice(map[string][]byte{"[Content_Types].xml": []byte(`<Types/>`), "word/document.xml": []byte(`<document/>`), "word/_rels/document.xml.rels": []byte(`<Relationship TargetMode="External" Target="http://unsafe"/>`)}), false},
		{"zip bomb", "document", "report.docx", "application/octet-stream", testOffice(map[string][]byte{"word/document.xml": bytes.Repeat([]byte("A"), 1<<20)}), false},
		{"arbitrary archive", "document", "file.zip", "application/zip", testOffice(map[string][]byte{"readme.txt": []byte("note")}), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t)
			record := testRecord(tc.data, tc.kind)
			record.Name, record.MIME = tc.filename, tc.mime
			id, err := s.Put("one", "message", "sender", record)
			if err != nil {
				t.Fatal(err)
			}
			f, _, _, err := s.Open(context.Background(), "one", id, func(_ context.Context, _ Record, out *LimitedFile) error { _, err := out.Write(tc.data); return err })
			if tc.safe {
				if err != nil {
					t.Fatal(err)
				}
				f.Close()
			} else {
				if err == nil {
					f.Close()
					t.Fatal("unsafe content accepted")
				}
				_, p, _ := s.paths("one", id)
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Fatal("rejected content persisted")
				}
			}
		})
	}
}

func TestAttachmentScannerFailsClosedAndCacheIntegrity(t *testing.T) {
	for _, scanner := range []Scanner{nil, rejectingScanner{ErrScanner}, rejectingScanner{ErrUnsafe}} {
		s := testStore(t)
		s.scanner = scanner
		data := testPNG()
		id := storeID(t, s, data, "image")
		f, _, _, err := s.Open(context.Background(), "one", id, func(_ context.Context, _ Record, out *LimitedFile) error { _, err := out.Write(data); return err })
		if err == nil {
			f.Close()
			t.Fatal("unscanned media accepted")
		}
	}
	s := testStore(t)
	data := testPNG()
	id := storeID(t, s, data, "image")
	f, _, _, err := s.Open(context.Background(), "one", id, func(_ context.Context, _ Record, out *LimitedFile) error { _, err := out.Write(data); return err })
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	_, path, _ := s.paths("one", id)
	data[len(data)-1] ^= 1
	os.WriteFile(path, data, 0600)
	if _, _, _, err := s.Open(context.Background(), "one", id, nil); !errors.Is(err, ErrUnsafe) {
		t.Fatal("tampered cache served")
	}
}

func TestAttachmentAccountSymlinkAndOrphanQuota(t *testing.T) {
	s := testStore(t)
	data := testPNG()
	if err := os.Symlink(t.TempDir(), filepath.Join(s.root, digest("one"))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("one", "id", "sender", testRecord(data, "image")); err == nil {
		t.Fatal("parent symlink allowed")
	}
	s = testStore(t)
	orphan, err := os.Create(filepath.Join(s.root, ".download-orphan"))
	if err != nil {
		t.Fatal(err)
	}
	orphan.Truncate(maxStoreBytes)
	orphan.Close()
	s, err = New(s.root, cleanScanner{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Put("one", "id", "sender", testRecord(data, "image")); !errors.Is(err, ErrFull) {
		t.Fatal("orphan bytes ignored")
	}
}

func TestClamdProtocolAndFailClosed(t *testing.T) {
	for _, verdict := range []string{"stream: OK\x00", "stream: Eicar-Test-Signature FOUND\x00", "stream: scanner error ERROR\x00", "bad reply\x00"} {
		t.Run(strings.TrimSuffix(verdict, "\x00"), func(t *testing.T) {
			// Short socket paths also work on macOS (Unix paths have a ~104 byte limit).
			root, err := os.MkdirTemp("", "wa-scan-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(root)
			path := filepath.Join(root, "s")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(2 * time.Second))
				cmd := make([]byte, 10)
				_, err = io.ReadFull(conn, cmd)
				if err != nil || string(cmd) != "zINSTREAM\x00" {
					done <- errors.New("bad command")
					return
				}
				var b bytes.Buffer
				for {
					var size [4]byte
					if _, err = io.ReadFull(conn, size[:]); err != nil {
						done <- err
						return
					}
					n := binary.BigEndian.Uint32(size[:])
					if n == 0 {
						break
					}
					if n > 32<<10 {
						done <- errors.New("unbounded chunk")
						return
					}
					if _, err = io.CopyN(&b, conn, int64(n)); err != nil {
						done <- err
						return
					}
				}
				if b.String() != "harmless bytes" {
					done <- errors.New("wrong content")
					return
				}
				_, err = io.WriteString(conn, verdict)
				done <- err
			}()
			err = (Clamd{Socket: path}).Scan(context.Background(), strings.NewReader("harmless bytes"))
			if verdict == "stream: OK\x00" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("unsafe/unknown scanner reply accepted")
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := (Clamd{Socket: "/nonexistent/clamd.ctl"}).Scan(context.Background(), strings.NewReader("x")); !errors.Is(err, ErrScanner) {
		t.Fatal("missing scanner accepted")
	}
}
