package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

// Container format (all integers big-endian):
//
//	magic[8]  "KNHBAK01"
//	salt[16]  argon2id salt
//	prefix[8] random nonce prefix
//	chunks:   len uint32 | AES-256-GCM(ciphertext+tag)
//
// Each chunk is sealed with nonce = prefix||counter(4) and additional data =
// 0x00 (more chunks follow) or 0x01 (final chunk). The final flag makes
// truncation detectable; the counter makes reordering detectable.
const (
	magic     = "KNHBAK01"
	chunkSize = 64 * 1024
	// MinPassphraseLen is the shortest passphrase accepted for a backup.
	MinPassphraseLen = 12

	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
)

// ErrBadPassphrase is returned when decryption fails authentication — either a
// wrong passphrase or a corrupted/truncated file.
var ErrBadPassphrase = errors.New("wrong passphrase or corrupted backup file")

func deriveKey(passphrase string, salt []byte) []byte {
	return argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, 32)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func chunkNonce(prefix []byte, counter uint32) []byte {
	n := make([]byte, 12)
	copy(n, prefix)
	binary.BigEndian.PutUint32(n[8:], counter)
	return n
}

// encryptWriter encrypts everything written to it into w. Close MUST be called
// to emit the final chunk.
type encryptWriter struct {
	w       io.Writer
	aead    cipher.AEAD
	prefix  []byte
	counter uint32
	buf     []byte
}

func newEncryptWriter(w io.Writer, passphrase string) (*encryptWriter, error) {
	salt := make([]byte, 16)
	prefix := make([]byte, 8)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(prefix); err != nil {
		return nil, err
	}
	aead, err := newGCM(deriveKey(passphrase, salt))
	if err != nil {
		return nil, err
	}
	hdr := append(append([]byte(magic), salt...), prefix...)
	if _, err := w.Write(hdr); err != nil {
		return nil, err
	}
	return &encryptWriter{w: w, aead: aead, prefix: prefix}, nil
}

func (e *encryptWriter) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		space := chunkSize - len(e.buf)
		n := min(space, len(p))
		e.buf = append(e.buf, p[:n]...)
		p = p[n:]
		if len(e.buf) == chunkSize {
			if err := e.flush(false); err != nil {
				return 0, err
			}
		}
	}
	return total, nil
}

func (e *encryptWriter) flush(final bool) error {
	ad := []byte{0}
	if final {
		ad[0] = 1
	}
	sealed := e.aead.Seal(nil, chunkNonce(e.prefix, e.counter), e.buf, ad)
	e.counter++
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(sealed)))
	if _, err := e.w.Write(l[:]); err != nil {
		return err
	}
	if _, err := e.w.Write(sealed); err != nil {
		return err
	}
	e.buf = e.buf[:0]
	return nil
}

// Close writes the final (possibly empty) chunk.
func (e *encryptWriter) Close() error { return e.flush(true) }

// decryptReader returns a reader over the plaintext of an encrypted container.
type decryptReader struct {
	r       io.Reader
	aead    cipher.AEAD
	prefix  []byte
	counter uint32
	plain   []byte
	done    bool
}

func newDecryptReader(r io.Reader, passphrase string) (*decryptReader, error) {
	hdr := make([]byte, len(magic)+16+8)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, fmt.Errorf("read backup header: %w", err)
	}
	if string(hdr[:len(magic)]) != magic {
		return nil, errors.New("not a Klever Node Hub backup file")
	}
	salt := hdr[len(magic) : len(magic)+16]
	prefix := hdr[len(magic)+16:]
	aead, err := newGCM(deriveKey(passphrase, salt))
	if err != nil {
		return nil, err
	}
	return &decryptReader{r: r, aead: aead, prefix: prefix}, nil
}

func (d *decryptReader) Read(p []byte) (int, error) {
	for len(d.plain) == 0 {
		if d.done {
			return 0, io.EOF
		}
		var l [4]byte
		if _, err := io.ReadFull(d.r, l[:]); err != nil {
			// A missing final chunk means truncation.
			return 0, fmt.Errorf("%w (truncated)", ErrBadPassphrase)
		}
		n := binary.BigEndian.Uint32(l[:])
		if n > chunkSize+uint32(d.aead.Overhead()) {
			return 0, ErrBadPassphrase
		}
		ct := make([]byte, n)
		if _, err := io.ReadFull(d.r, ct); err != nil {
			return 0, fmt.Errorf("%w (truncated)", ErrBadPassphrase)
		}
		nonce := chunkNonce(d.prefix, d.counter)
		pt, err := d.aead.Open(nil, nonce, ct, []byte{0})
		if err != nil {
			// Maybe it is the final chunk.
			pt, err = d.aead.Open(nil, nonce, ct, []byte{1})
			if err != nil {
				return 0, ErrBadPassphrase
			}
			d.done = true
			var extra [1]byte
			if _, err := io.ReadFull(d.r, extra[:]); !errors.Is(err, io.EOF) {
				return 0, fmt.Errorf("%w (trailing data)", ErrBadPassphrase)
			}
		}
		d.counter++
		d.plain = pt
	}
	n := copy(p, d.plain)
	d.plain = d.plain[n:]
	return n, nil
}
