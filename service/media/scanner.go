package media

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

var ErrUnsafe = errors.New("attachment rejected by security checks")
var ErrScanner = errors.New("attachment scanner unavailable")

// Scanner receives bytes, never an untrusted path or command line. A nil or
// unavailable scanner is a failure, not permission to serve the attachment.
type Scanner interface {
	Scan(context.Context, io.Reader) error
}

type Clamd struct{ Socket string }

func (c Clamd) Scan(ctx context.Context, content io.Reader) error {
	info, err := os.Lstat(c.Socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 {
		return ErrScanner
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return ErrScanner
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return ErrScanner
	}
	// Cancellation must also interrupt a scan already waiting on the daemon.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if _, err = io.WriteString(conn, "zINSTREAM\x00"); err != nil {
		return ErrScanner
	}
	var chunk [32 << 10]byte
	var size [4]byte
	var total int64
	for {
		n, readErr := content.Read(chunk[:])
		if n > 0 {
			total += int64(n)
			if total > MaxBytes {
				return ErrTooLarge
			}
			binary.BigEndian.PutUint32(size[:], uint32(n))
			if _, err = io.Copy(conn, bytes.NewReader(size[:])); err != nil {
				return ErrScanner
			}
			if _, err = io.Copy(conn, bytes.NewReader(chunk[:n])); err != nil {
				return ErrScanner
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return ErrScanner
		}
	}
	binary.BigEndian.PutUint32(size[:], 0)
	if _, err = conn.Write(size[:]); err != nil {
		return ErrScanner
	}
	response, err := bufio.NewReader(io.LimitReader(conn, 4097)).ReadString(0)
	if err != nil || len(response) > 4096 {
		return ErrScanner
	}
	if response == "stream: OK\x00" {
		return nil
	}
	if strings.HasSuffix(response, " FOUND\x00") {
		return ErrUnsafe
	}
	return ErrScanner
}
