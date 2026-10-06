package media

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	_ "golang.org/x/image/webp"
)

const MaxImagePixels = 16_000_000

// The same guards run for cached bytes. Native Whatsmeow verifies the original
// HMAC and hashes; this additional SHA check detects later disk corruption.
func (s *Store) validate(ctx context.Context, f *os.File, record Record) (string, error) {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return s.check(ctx, f, record)
}

func (s *Store) check(ctx context.Context, f *os.File, record Record) (string, error) {
	info, err := f.Stat()
	if err != nil {
		return "", ErrUnavailable
	}
	if info.Size() <= 0 || info.Size() > MaxBytes || uint64(info.Size()) != record.Size {
		return "", ErrTooLarge
	}
	h := sha256.New()
	if _, err = io.Copy(h, io.NewSectionReader(f, 0, info.Size())); err != nil {
		return "", ErrUnavailable
	}
	if !ValidID(record.SHA256) || hex.EncodeToString(h.Sum(nil)) != record.SHA256 {
		return "", ErrUnsafe
	}
	if s.scanner == nil {
		return "", ErrScanner
	}
	if err = s.scanner.Scan(ctx, io.NewSectionReader(f, 0, info.Size())); err != nil {
		return "", err
	}
	return inspect(f, record)
}

func inspect(f *os.File, record Record) (string, error) {
	var head [512]byte
	n, err := f.ReadAt(head[:], 0)
	if err != nil && err != io.EOF {
		return "", err
	}
	detected := strings.Split(http.DetectContentType(head[:n]), ";")[0]
	var extensions string
	switch record.Type {
	case "image", "sticker":
		if !strings.Contains("|image/jpeg|image/png|image/webp|image/gif|", "|"+detected+"|") {
			return "", ErrUnsafe
		}
		r := io.NewSectionReader(f, 0, int64(record.Size))
		config, _, err := image.DecodeConfig(r)
		if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 8192 || config.Height > 8192 || int64(config.Width)*int64(config.Height) > MaxImagePixels {
			return "", ErrUnsafe
		}
		r.Seek(0, io.SeekStart)
		if err = validateRaster(f, record, detected); err != nil {
			return "", ErrUnsafe
		}
		extensions = map[string]string{"image/jpeg": ".jpg|.jpeg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp"}[detected]
	case "video":
		if detected != "video/mp4" && detected != "video/webm" && detected != "video/3gpp" {
			return "", ErrUnsafe
		}
		extensions = map[string]string{"video/mp4": ".mp4|.m4v|.mov", "video/webm": ".webm", "video/3gpp": ".3gp"}[detected]
	case "audio":
		if detected == "video/mp4" {
			detected = "audio/mp4"
		}
		if detected == "video/webm" {
			detected = "audio/webm"
		}
		if detected == "application/ogg" {
			detected = "audio/ogg"
		}
		if n >= 2 && head[0] == 0xff && head[1]&0xf6 == 0xf0 {
			detected = "audio/aac"
		}
		if strings.HasPrefix(string(head[:n]), "#!AMR") {
			detected = "audio/amr"
		}
		extensions = map[string]string{"audio/aac": ".aac", "audio/amr": ".amr", "audio/ogg": ".ogg|.opus", "audio/mpeg": ".mp3", "audio/mp4": ".m4a|.mp4", "audio/wave": ".wav", "audio/webm": ".webm"}[detected]
		if extensions == "" {
			return "", ErrUnsafe
		}
	case "document":
		// No generic octet-stream fallback: scripts, executables, arbitrary ZIP,
		// HTML, SVG and unsupported office formats are deliberately refused.
		switch detected {
		case "application/pdf":
			if err = validatePDF(f, record.Size); err != nil {
				return "", err
			}
			extensions = ".pdf"
		case "application/zip":
			detected, err = validateOffice(f, record.Size, strings.ToLower(path.Ext(record.Name)))
			if err != nil {
				return "", err
			}
			extensions = strings.ToLower(path.Ext(record.Name))
		case "text/plain":
			ext := strings.ToLower(path.Ext(record.Name))
			if ext != ".txt" && ext != ".csv" {
				return "", ErrUnsafe
			}
			data, err := io.ReadAll(io.NewSectionReader(f, 0, int64(record.Size)))
			if err != nil || !utf8.Valid(data) || bytes.ContainsAny(data, "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x0b\x0c\x0e\x0f") {
				return "", ErrUnsafe
			}
			// Text files must never be a disguised script/markup or formula export.
			lower := bytes.ToLower(data)
			for _, token := range []string{"<script", "<html", "<svg", "<?php", "#!/", "powershell"} {
				if bytes.Contains(lower, []byte(token)) {
					return "", ErrUnsafe
				}
			}
			if ext == ".csv" {
				for len(data) > 0 {
					line, rest, _ := bytes.Cut(data, []byte("\n"))
					data = rest
					for len(line) > 0 {
						end := bytes.IndexAny(line, ",;\t")
						if end < 0 {
							end = len(line)
						}
						v := bytes.TrimLeft(line[:end], " \t\r\"")
						if len(v) > 0 && strings.ContainsRune("=+-@", rune(v[0])) {
							return "", ErrUnsafe
						}
						if end == len(line) {
							break
						}
						line = line[end+1:]
					}
				}
				detected = "text/csv"
			}
			extensions = ext
		default:
			return "", ErrUnsafe
		}
	default:
		return "", ErrUnsafe
	}
	// Sender-provided MIME is only a hint; it cannot override inspected bytes.
	if record.MIME != "" {
		claimed, _, err := mime.ParseMediaType(record.MIME)
		if err != nil {
			return "", ErrUnsafe
		}
		claimed = strings.ToLower(claimed)
		aliases := map[string]string{"image/jpg": "image/jpeg", "audio/x-wav": "audio/wave", "audio/wav": "audio/wave", "application/ogg": "audio/ogg", "video/mp4": "audio/mp4"}
		if canonical, ok := aliases[claimed]; ok && (record.Type == "audio" || claimed == "image/jpg") {
			claimed = canonical
		}
		if claimed != detected && claimed != "application/octet-stream" && !(detected == "text/csv" && claimed == "text/plain") {
			return "", ErrUnsafe
		}
	}
	if record.Name != "" {
		ext := strings.ToLower(path.Ext(record.Name))
		if !strings.Contains("|"+extensions+"|", "|"+ext+"|") || strings.ContainsAny(record.Name, ":%") {
			return "", ErrUnsafe
		}
		for _, part := range strings.Split(strings.ToLower(record.Name), ".") {
			if strings.Contains("|exe|com|bat|cmd|ps1|sh|php|phtml|js|vbs|scr|msi|hta|", "|"+part+"|") {
				return "", ErrUnsafe
			}
		}
	} else if record.Type == "document" {
		return "", ErrUnsafe
	}
	_, err = f.Seek(0, io.SeekStart)
	return detected, err
}

var pdfNames = regexp.MustCompile(`/([A-Za-z0-9#]+)`)

func validatePDF(f *os.File, size uint64) error {
	data, err := io.ReadAll(io.NewSectionReader(f, 0, int64(size)))
	if err != nil || !bytes.HasPrefix(data, []byte("%PDF-")) || !bytes.HasSuffix(bytes.TrimSpace(data), []byte("%%EOF")) {
		return ErrUnsafe
	}
	// Reject active objects, including escaped PDF name tokens. This is a
	// supplementary check, not a replacement for ClamAV or content disarm.
	for len(data) > 0 {
		index := pdfNames.FindSubmatchIndex(data)
		if index == nil {
			break
		}
		name := string(data[index[2]:index[3]])
		data = data[index[1]:]
		for i := 0; i+2 < len(name); i++ {
			if name[i] == '#' {
				if b, e := hex.DecodeString(name[i+1 : i+3]); e == nil {
					name = name[:i] + string(b) + name[i+3:]
				}
			}
		}
		switch name {
		case "JavaScript", "JS", "OpenAction", "AA", "Launch", "EmbeddedFile", "Encrypt", "RichMedia", "XFA":
			return ErrUnsafe
		}
	}
	return nil
}

func validateOffice(f *os.File, size uint64, extension string) (string, error) {
	types := map[string]string{".docx": "word/document.xml", ".xlsx": "xl/workbook.xml", ".pptx": "ppt/presentation.xml"}
	main, allowed := types[extension]
	if !allowed {
		return "", ErrUnsafe
	}
	z, err := zip.NewReader(f, int64(size))
	if err != nil || len(z.File) == 0 || len(z.File) > 1000 {
		return "", ErrUnsafe
	}
	var expanded uint64
	seen := map[string]bool{}
	for _, part := range z.File {
		name := part.Name
		lower := strings.ToLower(name)
		if seen[name] || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") || path.Clean(name) != strings.TrimSuffix(name, "/") || !part.Mode().IsRegular() && !part.FileInfo().IsDir() {
			return "", ErrUnsafe
		}
		seen[name] = true
		if part.Flags&1 != 0 || part.UncompressedSize64 > 16<<20 || part.UncompressedSize64 > (part.CompressedSize64+1)*100 {
			return "", ErrUnsafe
		}
		expanded += part.UncompressedSize64
		if expanded > 64<<20 {
			return "", ErrUnsafe
		}
		for _, marker := range []string{"vbaproject", "macros", "embeddings/", "activex/", "externallinks/"} {
			if strings.Contains(lower, marker) {
				return "", ErrUnsafe
			}
		}
		if part.FileInfo().IsDir() {
			continue
		}
		ext := strings.ToLower(path.Ext(name))
		if !strings.Contains("|.xml|.rels|.png|.jpg|.jpeg|.gif|.webp|", "|"+ext+"|") {
			return "", ErrUnsafe
		}
		r, err := part.Open()
		if err != nil {
			return "", ErrUnsafe
		}
		if ext == ".xml" || ext == ".rels" {
			decoder := xml.NewDecoder(io.LimitReader(r, 16<<20+1))
			depth := 0
			for {
				token, e := decoder.Token()
				if e == io.EOF {
					break
				}
				if e != nil {
					r.Close()
					return "", ErrUnsafe
				}
				switch t := token.(type) {
				case xml.Directive:
					r.Close()
					return "", ErrUnsafe
				case xml.StartElement:
					depth++
					if depth > 100 {
						r.Close()
						return "", ErrUnsafe
					}
					for _, attr := range t.Attr {
						if attr.Name.Local == "TargetMode" && strings.EqualFold(attr.Value, "External") || strings.Contains(strings.ToLower(attr.Value), "macroenabled") {
							r.Close()
							return "", ErrUnsafe
						}
					}
				case xml.EndElement:
					depth--
				}
			}
		} else {
			_, err = io.Copy(io.Discard, io.LimitReader(r, 16<<20+1))
		}
		r.Close()
		if err != nil {
			return "", ErrUnsafe
		}
	}
	if !seen["[Content_Types].xml"] || !seen[main] {
		return "", ErrUnsafe
	}
	return map[string]string{".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation"}[extension], nil
}
