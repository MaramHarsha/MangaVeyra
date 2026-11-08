package importer

import (
	"archive/zip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
)

// ZipLimits bound what opening a zip may cost before any entry is read.
// archive/zip parses the whole central directory into memory when a zip is
// opened (a few hundred bytes of heap per entry), so a crafted upload made of
// nothing but directory headers could exhaust memory before the entry limit
// is checked. OpenZip refuses such files first.
type ZipLimits struct {
	// MaxEntries is the largest entry count the zip may declare or hold.
	MaxEntries int
	// MaxDirectoryBytes is the largest central directory the zip may declare
	// or that is read while opening it.
	MaxDirectoryBytes int64
}

// Errors returned by OpenZip before the central directory is parsed.
var (
	ErrZipTooManyEntries    = errors.New("zip: the archive declares too many entries")
	ErrZipDirectoryTooLarge = errors.New("zip: the archive's table of contents is too large")
)

// zipLimits are the limits an import archive is opened with.
func (l Limits) zipLimits() ZipLimits {
	return ZipLimits{MaxEntries: l.MaxEntries, MaxDirectoryBytes: int64(l.MaxEntries) * maxDirectoryEntryBytes}
}

// maxDirectoryEntryBytes is the room allowed per central directory entry: the
// 46-byte header plus a generous name and extra fields.
const maxDirectoryEntryBytes = 1 << 10

// directorySlack covers what archive/zip reads besides the central directory
// while opening: the end record search (1 KiB, then 65 KiB), the zip64
// records, one probe header (up to 3 × 64 KiB of name, extra and comment) and
// the read-ahead of its 4 KiB buffer.
const directorySlack = 320 << 10

// OpenZip opens the zip at path within lim. It first reads the end of central
// directory record (and the zip64 record it points to) and refuses an archive
// that declares more entries or a larger directory than allowed. Because the
// declared count is only checked modulo 65536 by archive/zip, which reads
// headers until one fails to parse, the directory is then read through a
// byte budget: a directory larger than declared fails instead of growing.
func OpenZip(path string, lim ZipLimits) (*zip.Reader, io.Closer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if err := checkDirectoryEnd(f, fi.Size(), lim); err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	br := &budgetReader{r: f}
	br.left.Store(lim.MaxDirectoryBytes + directorySlack)
	zr, err := zip.NewReader(br, fi.Size())
	if err != nil {
		_ = f.Close()
		if errors.Is(err, errBudget) {
			return nil, nil, ErrZipDirectoryTooLarge
		}
		return nil, nil, err
	}
	br.left.Store(-1) // entry contents are bounded by the callers' own limits
	if len(zr.File) > lim.MaxEntries {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%w: %d entries (at most %d)", ErrZipTooManyEntries, len(zr.File), lim.MaxEntries)
	}
	return zr, f, nil
}

var errBudget = errors.New("zip: read budget exhausted")

// budgetReader fails reads once left bytes were read (left < 0: unlimited).
type budgetReader struct {
	r    io.ReaderAt
	left atomic.Int64
}

func (b *budgetReader) ReadAt(p []byte, off int64) (int, error) {
	if left := b.left.Load(); left >= 0 {
		if int64(len(p)) > left {
			return 0, errBudget
		}
		b.left.Add(-int64(len(p)))
	}
	return b.r.ReadAt(p, off)
}

const (
	eocdSignature       = 0x06054b50
	eocdLen             = 22
	zip64LocSignature   = 0x07064b50
	zip64LocLen         = 20
	zip64EndSignature   = 0x06064b50
	zip64EndLen         = 56
	maxEOCDSearchWindow = 65 * 1024
)

// checkDirectoryEnd reads the declared entry count and central directory size
// the way archive/zip finds them (the last end record in the final 1 KiB, else
// the final 65 KiB; the zip64 record when a field is saturated). A file
// without an end record is left for archive/zip to refuse as damaged.
func checkDirectoryEnd(r io.ReaderAt, size int64, lim ZipLimits) error {
	var buf []byte
	var at int64 = -1
	for _, n := range []int64{1024, maxEOCDSearchWindow} {
		n = min(n, size)
		if n < eocdLen {
			return nil
		}
		buf = make([]byte, n)
		if _, err := r.ReadAt(buf, size-n); err != nil && err != io.EOF {
			return err
		}
		if p := lastEOCD(buf); p >= 0 {
			at, buf = size-n+int64(p), buf[p:]
			break
		}
		if n == size {
			break
		}
	}
	if at < 0 {
		return nil
	}
	le := binary.LittleEndian
	records, dirSize := uint64(le.Uint16(buf[10:])), uint64(le.Uint32(buf[12:]))
	if records == 0xffff || dirSize == 0xffffffff || le.Uint32(buf[16:]) == 0xffffffff {
		if r64, s64, ok := zip64DirectoryEnd(r, at); ok {
			records, dirSize = r64, s64
		}
	}
	if records > uint64(lim.MaxEntries) {
		return fmt.Errorf("%w: %d entries (at most %d)", ErrZipTooManyEntries, records, lim.MaxEntries)
	}
	if dirSize > uint64(lim.MaxDirectoryBytes) {
		return fmt.Errorf("%w: %d bytes (at most %d)", ErrZipDirectoryTooLarge, dirSize, lim.MaxDirectoryBytes)
	}
	return nil
}

// lastEOCD finds the last end record whose comment fits in b.
func lastEOCD(b []byte) int {
	for i := len(b) - eocdLen; i >= 0; i-- {
		if binary.LittleEndian.Uint32(b[i:]) == eocdSignature {
			if n := int(binary.LittleEndian.Uint16(b[i+eocdLen-2:])); i+eocdLen+n <= len(b) {
				return i
			}
			return -1
		}
	}
	return -1
}

// zip64DirectoryEnd reads the zip64 entry count and directory size through the
// locator just before the end record at eocd.
func zip64DirectoryEnd(r io.ReaderAt, eocd int64) (records, size uint64, ok bool) {
	if eocd < zip64LocLen {
		return 0, 0, false
	}
	loc := make([]byte, zip64LocLen)
	if _, err := r.ReadAt(loc, eocd-zip64LocLen); err != nil {
		return 0, 0, false
	}
	le := binary.LittleEndian
	if le.Uint32(loc) != zip64LocSignature || le.Uint32(loc[4:]) != 0 || le.Uint32(loc[16:]) != 1 {
		return 0, 0, false
	}
	off := le.Uint64(loc[8:])
	if off > 1<<62 {
		return 0, 0, false
	}
	end := make([]byte, zip64EndLen)
	if _, err := r.ReadAt(end, int64(off)); err != nil || le.Uint32(end) != zip64EndSignature {
		return 0, 0, false
	}
	return le.Uint64(end[32:]), le.Uint64(end[40:]), true
}
