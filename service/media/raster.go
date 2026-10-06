package media

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/gif"
	"io"
	"os"
)

func dimensions(width, height int) bool {
	return width > 0 && height > 0 && width <= 8192 && height <= 8192 && int64(width)*int64(height) <= MaxImagePixels
}

func validateRaster(f *os.File, record Record, mime string) error {
	if mime == "image/webp" {
		data, err := io.ReadAll(io.NewSectionReader(f, 0, int64(record.Size)))
		if err != nil {
			return ErrUnsafe
		}
		return validateWebP(data)
	}
	if mime == "image/gif" {
		data, err := io.ReadAll(io.NewSectionReader(f, 0, int64(record.Size)))
		if err != nil {
			return ErrUnsafe
		}
		if !gifBounds(data) {
			return ErrUnsafe
		}
		if _, err := gif.DecodeAll(bytes.NewReader(data)); err != nil {
			return ErrUnsafe
		}
		return nil
	}
	if _, _, err := image.Decode(io.NewSectionReader(f, 0, int64(record.Size))); err != nil {
		return ErrUnsafe
	}
	return nil
}

// DecodeConfig alone is insufficient for animation: embedded frame headers can
// allocate more pixels than the advertised canvas. Validate every frame first.
func gifBounds(data []byte) bool {
	if len(data) < 13 {
		return false
	}
	w, h := int(binary.LittleEndian.Uint16(data[6:8])), int(binary.LittleEndian.Uint16(data[8:10]))
	if !dimensions(w, h) {
		return false
	}
	pos := 13
	if data[10]&128 != 0 {
		pos += 3 * (1 << ((data[10] & 7) + 1))
	}
	frames := 0
	pixels := int64(0)
	skip := func() bool {
		for {
			if pos >= len(data) {
				return false
			}
			n := int(data[pos])
			pos++
			if n == 0 {
				return true
			}
			pos += n
			if pos > len(data) {
				return false
			}
		}
	}
	for pos < len(data) {
		kind := data[pos]
		pos++
		switch kind {
		case 0x3b:
			return pos == len(data) && frames > 0
		case 0x21:
			if pos >= len(data) {
				return false
			}
			pos++
			if !skip() {
				return false
			}
		case 0x2c:
			if len(data)-pos < 9 {
				return false
			}
			d := data[pos : pos+9]
			pos += 9
			x, y, fw, fh := int(binary.LittleEndian.Uint16(d[0:2])), int(binary.LittleEndian.Uint16(d[2:4])), int(binary.LittleEndian.Uint16(d[4:6])), int(binary.LittleEndian.Uint16(d[6:8]))
			frames++
			pixels += int64(fw) * int64(fh)
			if frames > 500 || !dimensions(fw, fh) || x+fw > w || y+fh > h || pixels > MaxImagePixels {
				return false
			}
			if d[8]&128 != 0 {
				pos += 3 * (1 << ((d[8] & 7) + 1))
			}
			if pos >= len(data) {
				return false
			}
			pos++
			if !skip() {
				return false
			}
		default:
			return false
		}
	}
	return false
}

type webPChunk struct {
	kind string
	data []byte
}

func webPChunks(data []byte) ([]webPChunk, bool) {
	var chunks []webPChunk
	for len(data) > 0 {
		if len(data) < 8 || len(chunks) >= 1000 {
			return nil, false
		}
		n := uint64(binary.LittleEndian.Uint32(data[4:8]))
		padded := n + n%2
		if padded > uint64(len(data)-8) {
			return nil, false
		}
		chunks = append(chunks, webPChunk{string(data[:4]), data[8 : 8+n]})
		data = data[8+padded:]
	}
	return chunks, true
}
func webPU24(data []byte) int { return int(data[0]) | int(data[1])<<8 | int(data[2])<<16 }
func webPFrame(chunks []webPChunk, width, height int) error {
	var body bytes.Buffer
	body.WriteString("WEBP")
	alpha := false
	images := 0
	for _, part := range chunks {
		if part.kind == "ALPH" {
			alpha = true
		} else if part.kind != "VP8 " && part.kind != "VP8L" {
			return ErrUnsafe
		}
		if part.kind == "VP8 " || part.kind == "VP8L" {
			images++
			// Inspect compressed dimensions without trusting VP8X or ANMF.
			var bare bytes.Buffer
			bare.WriteString("RIFF")
			binary.Write(&bare, binary.LittleEndian, uint32(12+len(part.data)+len(part.data)%2))
			bare.WriteString("WEBP")
			bare.WriteString(part.kind)
			binary.Write(&bare, binary.LittleEndian, uint32(len(part.data)))
			bare.Write(part.data)
			if len(part.data)%2 != 0 {
				bare.WriteByte(0)
			}
			config, _, err := image.DecodeConfig(bytes.NewReader(bare.Bytes()))
			if err != nil || !dimensions(config.Width, config.Height) || config.Width != width || config.Height != height {
				return ErrUnsafe
			}
		}
	}
	if images != 1 {
		return ErrUnsafe
	}
	if alpha {
		header := make([]byte, 10)
		header[0] = 16
		for i := 0; i < 3; i++ {
			header[4+i] = byte((width - 1) >> (i * 8))
			header[7+i] = byte((height - 1) >> (i * 8))
		}
		body.WriteString("VP8X")
		binary.Write(&body, binary.LittleEndian, uint32(10))
		body.Write(header)
	}
	for _, part := range chunks {
		body.WriteString(part.kind)
		binary.Write(&body, binary.LittleEndian, uint32(len(part.data)))
		body.Write(part.data)
		if len(part.data)%2 != 0 {
			body.WriteByte(0)
		}
	}
	var full bytes.Buffer
	full.WriteString("RIFF")
	binary.Write(&full, binary.LittleEndian, uint32(body.Len()))
	full.Write(body.Bytes())
	if _, _, err := image.Decode(bytes.NewReader(full.Bytes())); err != nil {
		return ErrUnsafe
	}
	return nil
}
func validateWebP(data []byte) error {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" || uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data)) {
		return ErrUnsafe
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || !dimensions(config.Width, config.Height) {
		return ErrUnsafe
	}
	chunks, ok := webPChunks(data[12:])
	if !ok {
		return ErrUnsafe
	}
	animated := false
	frames := 0
	pixels := int64(0)
	var still []webPChunk
	for _, part := range chunks {
		switch part.kind {
		case "VP8X":
			if len(part.data) != 10 {
				return ErrUnsafe
			}
			animated = part.data[0]&2 != 0
		case "ANIM":
			if !animated || len(part.data) != 6 {
				return ErrUnsafe
			}
		case "ANMF":
			if !animated || len(part.data) < 16 {
				return ErrUnsafe
			}
			d := part.data
			w, h := webPU24(d[6:9])+1, webPU24(d[9:12])+1
			frames++
			pixels += int64(w) * int64(h)
			if frames > 500 || !dimensions(w, h) || 2*webPU24(d[:3])+w > config.Width || 2*webPU24(d[3:6])+h > config.Height || pixels > 64_000_000 {
				return ErrUnsafe
			}
			nested, ok := webPChunks(d[16:])
			if !ok || webPFrame(nested, w, h) != nil {
				return ErrUnsafe
			}
		case "VP8 ", "VP8L", "ALPH":
			still = append(still, part)
		case "ICCP", "EXIF", "XMP ": // Bounded metadata; scanned with the rest of the file.
		default:
			return ErrUnsafe
		}
	}
	if animated {
		if frames == 0 || len(still) != 0 {
			return ErrUnsafe
		}
		return nil
	}
	return webPFrame(still, config.Width, config.Height)
}
