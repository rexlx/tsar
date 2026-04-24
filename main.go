//go:build js && wasm

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"syscall/js"

	"github.com/yeka/zip"
	"golang.org/x/crypto/pbkdf2"
)

const maxDecompressSize = 512 * 1024 * 1024

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func deriveKey(password string, salt []byte) []byte {
	return pbkdf2.Key([]byte(password), salt, 100000, 32, sha256.New)
}

func jsToBytes(v js.Value) []byte {
	len := v.Get("byteLength").Int()
	buf := make([]byte, len)
	js.CopyBytesToGo(buf, v)
	return buf
}

func bytesToJS(b []byte) js.Value {
	uint8Array := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(uint8Array, b)
	return uint8Array
}

func encryptWrap(this js.Value, args []js.Value) any {
	plainData := jsToBytes(args[0])
	defer zero(plainData)
	password := args[1].String()

	salt := make([]byte, 16)
	rand.Read(salt)
	key := deriveKey(password, salt)
	defer zero(key)

	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	rand.Read(nonce)

	ciphertext := gcm.Seal(nil, nonce, plainData, nil)
	final := append(salt, nonce...)
	final = append(final, ciphertext...)

	return bytesToJS(final)
}

func decryptWrap(this js.Value, args []js.Value) any {
	encData := jsToBytes(args[0])
	password := args[1].String()

	if len(encData) < 28 {
		return nil
	}

	salt := encData[:16]
	nonce := encData[16:28]
	ciphertext := encData[28:]

	key := deriveKey(password, salt)
	defer zero(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil
	}
	gcm, _ := cipher.NewGCM(block)

	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil
	}

	jsResult := bytesToJS(plain)
	zero(plain)
	return jsResult
}

func listArchive(this js.Value, args []js.Value) any {
	data := jsToBytes(args[0])
	format := args[1].String()
	var filenames []any

	if format == "zip" {
		r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil
		}
		for _, f := range r.File {
			filenames = append(filenames, f.Name)
		}
	} else {
		var r io.Reader = bytes.NewReader(data)
		if format == "tgz" {
			gr, err := gzip.NewReader(r)
			if err != nil {
				return nil
			}
			defer gr.Close()
			r = gr
		}
		tr := tar.NewReader(r)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}
			filenames = append(filenames, hdr.Name)
		}
	}
	return js.ValueOf(filenames)
}

func extractFile(this js.Value, args []js.Value) any {
	archiveData := jsToBytes(args[0])
	targetName := args[1].String()
	format := args[2].String()
	password := args[3].String()

	if strings.Contains(targetName, "..") || strings.HasPrefix(targetName, "/") {
		return nil
	}

	if format == "zip" {
		r, _ := zip.NewReader(bytes.NewReader(archiveData), int64(len(archiveData)))
		for _, f := range r.File {
			if f.Name == targetName {
				if f.UncompressedSize64 > maxDecompressSize {
					return nil
				}
				if f.IsEncrypted() {
					f.SetPassword(password)
				}
				rc, err := f.Open()
				if err != nil {
					return nil
				}
				defer rc.Close()
				buf := new(bytes.Buffer)
				io.Copy(buf, rc)
				res := buf.Bytes()
				jsRes := bytesToJS(res)
				zero(res)
				return jsRes
			}
		}
	} else {
		var r io.Reader = bytes.NewReader(archiveData)
		if format == "tgz" {
			gr, _ := gzip.NewReader(r)
			r = gr
		}
		tr := tar.NewReader(r)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if hdr.Name == targetName {
				if hdr.Size > maxDecompressSize {
					return nil
				}
				buf := new(bytes.Buffer)
				io.Copy(buf, io.LimitReader(tr, maxDecompressSize))
				res := buf.Bytes()
				jsRes := bytesToJS(res)
				zero(res)
				return jsRes
			}
		}
	}
	return nil
}

func compressFiles(this js.Value, args []js.Value) any {
	files := args[0]
	format := args[1].String()
	password := args[2].String()
	buf := new(bytes.Buffer)

	if format == "zip" {
		zw := zip.NewWriter(buf)
		keys := js.Global().Get("Object").Call("keys", files)
		for i := 0; i < keys.Length(); i++ {
			name := keys.Index(i).String()
			content := jsToBytes(files.Get(name))
			var w io.Writer
			if password != "" {
				w, _ = zw.Encrypt(name, password, zip.AES256Encryption)
			} else {
				w, _ = zw.Create(name)
			}
			w.Write(content)
			zero(content)
		}
		zw.Close()
	} else {
		var tw *tar.Writer
		var gw *gzip.Writer
		if format == "tgz" {
			gw = gzip.NewWriter(buf)
			tw = tar.NewWriter(gw)
		} else {
			tw = tar.NewWriter(buf)
		}

		keys := js.Global().Get("Object").Call("keys", files)
		for i := 0; i < keys.Length(); i++ {
			name := keys.Index(i).String()
			content := jsToBytes(files.Get(name))
			hdr := &tar.Header{Name: name, Size: int64(len(content))}
			tw.WriteHeader(hdr)
			tw.Write(content)
			zero(content)
		}
		tw.Close()
		if gw != nil {
			gw.Close()
		}
	}
	return bytesToJS(buf.Bytes())
}

func main() {
	keepAlive := make(chan struct{}, 0)
	js.Global().Set("listArchive", js.FuncOf(listArchive))
	js.Global().Set("extractFile", js.FuncOf(extractFile))
	js.Global().Set("compressFiles", js.FuncOf(compressFiles))
	js.Global().Set("encryptWrap", js.FuncOf(encryptWrap))
	js.Global().Set("decryptWrap", js.FuncOf(decryptWrap))
	fmt.Println("Go Archive System Online")
	<-keepAlive
}
