package media

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"os"
	"testing"
)

func TestRasterAnimationFramesStayBounded(t *testing.T) {
	// The static WebP fixture is from golang.org/x/image (BSD-3-Clause).
	data, err := os.ReadFile("testdata/lossless.webp")
	if err != nil {
		t.Fatal(err)
	}
	if err = validateWebP(data); err != nil {
		t.Fatal("valid WebP rejected", err)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	chunks, ok := webPChunks(data[12:])
	if !ok {
		t.Fatal("bad fixture")
	}
	var compressed bytes.Buffer
	for _, c := range chunks {
		if c.kind == "VP8L" || c.kind == "VP8 " {
			compressed.WriteString(c.kind)
			binary.Write(&compressed, binary.LittleEndian, uint32(len(c.data)))
			compressed.Write(c.data)
			if len(c.data)%2 != 0 {
				compressed.WriteByte(0)
			}
		}
	}
	var body bytes.Buffer
	body.WriteString("WEBP")
	chunk := func(name string, b []byte) {
		body.WriteString(name)
		binary.Write(&body, binary.LittleEndian, uint32(len(b)))
		body.Write(b)
		if len(b)%2 != 0 {
			body.WriteByte(0)
		}
	}
	header := make([]byte, 10)
	header[0] = 2
	put24 := func(b []byte, n int) {
		for i := 0; i < 3; i++ {
			b[i] = byte(n >> (8 * i))
		}
	}
	put24(header[4:7], config.Width-1)
	put24(header[7:10], config.Height-1)
	chunk("VP8X", header)
	chunk("ANIM", make([]byte, 6))
	frame := make([]byte, 16)
	put24(frame[6:9], config.Width-1)
	put24(frame[9:12], config.Height-1)
	chunk("ANMF", append(frame, compressed.Bytes()...))
	chunk("ANMF", append(frame, compressed.Bytes()...))
	var animated bytes.Buffer
	animated.WriteString("RIFF")
	binary.Write(&animated, binary.LittleEndian, uint32(body.Len()))
	animated.Write(body.Bytes())
	if err = validateWebP(animated.Bytes()); err != nil {
		t.Fatal("animated sticker rejected", err)
	}
	bomb := append([]byte(nil), animated.Bytes()...)
	copy(bomb[24:27], []byte{255, 255, 255})
	if validateWebP(bomb) == nil {
		t.Fatal("WebP dimension bomb accepted")
	}
	for n := 1; n < len(animated.Bytes()); n += 7 {
		if validateWebP(animated.Bytes()[:n]) == nil {
			t.Fatal("truncated WebP accepted")
		}
	}
	p := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	var g bytes.Buffer
	gif.EncodeAll(&g, &gif.GIF{Image: []*image.Paletted{p, p}, Delay: []int{1, 1}})
	if !gifBounds(g.Bytes()) {
		t.Fatal("animated GIF rejected")
	}
	if gifBounds(g.Bytes()[:g.Len()-1]) {
		t.Fatal("truncated GIF accepted")
	}
	huge := append([]byte(nil), g.Bytes()...)
	huge[6], huge[7] = 255, 255
	if gifBounds(huge) {
		t.Fatal("GIF dimensions accepted")
	}
}
