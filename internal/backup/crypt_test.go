package backup

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"
)

var fast = Params{LogN: 10, R: 8, P: 1}

func seal(t *testing.T, pass string, data []byte) []byte {
	t.Helper()
	k, err := DeriveKey(pass, fast)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	w, err := NewWriter(&out, k)
	if err != nil {
		t.Fatal(err)
	}
	// Uneven writes across chunk borders.
	for len(data) > 0 {
		n := min(len(data), 10000)
		if _, err := w.Write(data[:n]); err != nil {
			t.Fatal(err)
		}
		data = data[n:]
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func open(pass string, sealed []byte) ([]byte, error) {
	r, err := NewReader(bytes.NewReader(sealed), pass)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func TestCryptRoundTrip(t *testing.T) {
	for _, size := range []int{0, 1, ChunkSize - 1, ChunkSize, ChunkSize + 1, 3*ChunkSize + 17} {
		data := make([]byte, size)
		rand.Read(data)
		sealed := seal(t, "correct horse battery", data)
		got, err := open("correct horse battery", sealed)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("size %d: %v", size, err)
		}
		if size > 64 && bytes.Contains(sealed, data[:64]) {
			t.Fatal("明文不能出现在文件里")
		}
	}
}

func TestCryptRejects(t *testing.T) {
	data := make([]byte, 2*ChunkSize+5)
	rand.Read(data)
	sealed := seal(t, "correct horse battery", data)
	if _, err := open("wrong horse battery", sealed); !errors.Is(err, ErrPassphrase) {
		t.Fatal("口令错要报出来", err)
	}
	// 截掉最后一块：不能当成完整的备份。
	chunk := 4 + ChunkSize + 16
	header := len(magic) + 3 + 16 + 8
	if _, err := open("correct horse battery", sealed[:header+2*chunk]); err == nil {
		t.Fatal("截断的文件要报错")
	}
	tampered := append([]byte{}, sealed...)
	tampered[header+chunk+100] ^= 1
	if _, err := open("correct horse battery", tampered); err == nil {
		t.Fatal("改动过的文件要报错")
	}
	if _, err := open("x", []byte("not a backup at all, definitely")); err == nil {
		t.Fatal("别的文件要报错")
	}
}
