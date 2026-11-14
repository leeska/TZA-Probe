// Package backup is the backup module: a daily, passphrase-encrypted
// snapshot of the Server's data directory, written to nodes over the
// management channel, and the restore that reads it back.
package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/scrypt"
)

// File format (all integers big-endian):
//
//	"TZABK1"  magic
//	1 byte    log2 N, 1 byte r, 1 byte p   scrypt parameters
//	16 bytes  salt
//	8 bytes   nonce prefix
//	chunks:   uint32 length | AES-256-GCM ciphertext of up to ChunkSize bytes
//
// The length's top bit marks the last chunk, which is also bound into the
// chunk's additional data, so a truncated file never decrypts as complete.
// Chunk i uses nonce prefix ‖ uint32(i).
const (
	magic     = "TZABK1"
	ChunkSize = 64 << 10
	lastFlag  = 1 << 31
	// Extension of backup files.
	Extension = ".tzabk"
)

// Params are the scrypt cost parameters.
type Params struct {
	LogN, R, P uint8
}

// DefaultParams cost about 100 ms and 32 MB per derivation.
var DefaultParams = Params{LogN: 15, R: 8, P: 1}

// Key is a derived encryption key with the salt and parameters it came from.
type Key struct {
	Params Params
	Salt   [16]byte
	Key    [32]byte
}

// DeriveKey makes a key from a passphrase with a fresh salt.
func DeriveKey(passphrase string, params Params) (Key, error) {
	var k Key
	k.Params = params
	if _, err := rand.Read(k.Salt[:]); err != nil {
		return k, err
	}
	return k, k.derive(passphrase)
}

func (k *Key) derive(passphrase string) error {
	if k.Params.LogN < 10 || k.Params.LogN > 22 || k.Params.R == 0 || k.Params.P == 0 {
		return errors.New("备份文件的密钥参数无效")
	}
	raw, err := scrypt.Key([]byte(passphrase), k.Salt[:], 1<<k.Params.LogN, int(k.Params.R), int(k.Params.P), 32)
	if err != nil {
		return err
	}
	copy(k.Key[:], raw)
	return nil
}

func gcm(key [32]byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func nonce(prefix [8]byte, i uint32) []byte {
	n := make([]byte, 12)
	copy(n, prefix[:])
	binary.BigEndian.PutUint32(n[8:], i)
	return n
}

func aad(last bool) []byte {
	if last {
		return []byte{1}
	}
	return []byte{0}
}

// Writer encrypts what is written to it; Close writes the last chunk and
// must be called.
type Writer struct {
	w      io.Writer
	aead   cipher.AEAD
	prefix [8]byte
	i      uint32
	buf    []byte
	err    error
}

// NewWriter writes the header for k to w and returns the encrypting writer.
func NewWriter(w io.Writer, k Key) (*Writer, error) {
	aead, err := gcm(k.Key)
	if err != nil {
		return nil, err
	}
	enc := &Writer{w: w, aead: aead, buf: make([]byte, 0, ChunkSize)}
	if _, err := rand.Read(enc.prefix[:]); err != nil {
		return nil, err
	}
	header := append([]byte(magic), k.Params.LogN, k.Params.R, k.Params.P)
	header = append(header, k.Salt[:]...)
	header = append(header, enc.prefix[:]...)
	if _, err := w.Write(header); err != nil {
		return nil, err
	}
	return enc, nil
}

func (e *Writer) chunk(last bool) error {
	if e.i == ^uint32(0) {
		return errors.New("备份太大")
	}
	sealed := e.aead.Seal(nil, nonce(e.prefix, e.i), e.buf, aad(last))
	e.i++
	n := uint32(len(sealed))
	if last {
		n |= lastFlag
	}
	var head [4]byte
	binary.BigEndian.PutUint32(head[:], n)
	if _, err := e.w.Write(head[:]); err != nil {
		return err
	}
	_, err := e.w.Write(sealed)
	e.buf = e.buf[:0]
	return err
}

func (e *Writer) Write(p []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	written := 0
	for len(p) > 0 {
		room := ChunkSize - len(e.buf)
		take := min(room, len(p))
		e.buf = append(e.buf, p[:take]...)
		p, written = p[take:], written+take
		if len(e.buf) == ChunkSize {
			if e.err = e.chunk(false); e.err != nil {
				return written, e.err
			}
		}
	}
	return written, nil
}

// Close seals the last (possibly empty) chunk.
func (e *Writer) Close() error {
	if e.err != nil {
		return e.err
	}
	e.err = e.chunk(true)
	if e.err == nil {
		e.err = errors.New("已关闭")
		return nil
	}
	return e.err
}

// ErrPassphrase is a wrong passphrase (or a damaged first chunk).
var ErrPassphrase = errors.New("口令不对，或备份文件已损坏")

// Reader decrypts a backup.
type Reader struct {
	r      io.Reader
	aead   cipher.AEAD
	prefix [8]byte
	i      uint32
	plain  []byte
	done   bool
}

// NewReader reads the header from r and derives the key from passphrase.
func NewReader(r io.Reader, passphrase string) (*Reader, error) {
	header := make([]byte, len(magic)+3+16+8)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, errors.New("不是 TZA 备份文件（文件太短）")
	}
	if string(header[:len(magic)]) != magic {
		return nil, errors.New("不是 TZA 备份文件")
	}
	rest := header[len(magic):]
	var k Key
	k.Params = Params{LogN: rest[0], R: rest[1], P: rest[2]}
	copy(k.Salt[:], rest[3:19])
	if err := k.derive(passphrase); err != nil {
		return nil, err
	}
	aead, err := gcm(k.Key)
	if err != nil {
		return nil, err
	}
	dec := &Reader{r: r, aead: aead}
	copy(dec.prefix[:], rest[19:27])
	// Open the first chunk now: a wrong passphrase shows before anything is
	// written anywhere.
	if err := dec.next(); err != nil {
		return nil, err
	}
	return dec, nil
}

func (d *Reader) Read(p []byte) (int, error) {
	for len(d.plain) == 0 {
		if d.done {
			return 0, io.EOF
		}
		if err := d.next(); err != nil {
			return 0, err
		}
	}
	n := copy(p, d.plain)
	d.plain = d.plain[n:]
	return n, nil
}

// next decrypts the following chunk into d.plain.
func (d *Reader) next() error {
	var head [4]byte
	if _, err := io.ReadFull(d.r, head[:]); err != nil {
		return errors.New("备份文件不完整")
	}
	n := binary.BigEndian.Uint32(head[:])
	last := n&lastFlag != 0
	n &^= lastFlag
	if n < uint32(d.aead.Overhead()) || n > ChunkSize+uint32(d.aead.Overhead()) {
		return errors.New("备份文件已损坏")
	}
	sealed := make([]byte, n)
	if _, err := io.ReadFull(d.r, sealed); err != nil {
		return errors.New("备份文件不完整")
	}
	plain, err := d.aead.Open(nil, nonce(d.prefix, d.i), sealed, aad(last))
	if err != nil {
		if d.i == 0 {
			return ErrPassphrase
		}
		return fmt.Errorf("备份文件第 %d 块已损坏", d.i)
	}
	d.i++
	d.plain, d.done = plain, last
	return nil
}
