//go:build js && wasm

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"syscall/js"
)

// --- Helper Functions ---

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

// --- Exported Functions ---

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
			gr, _ := gzip.NewReader(r)
			r = gr
		}
		tr := tar.NewReader(r)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
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

	if format == "zip" {
		r, _ := zip.NewReader(bytes.NewReader(archiveData), int64(len(archiveData)))
		for _, f := range r.File {
			if f.Name == targetName {
				rc, _ := f.Open()
				defer rc.Close()
				buf := new(bytes.Buffer)
				io.Copy(buf, rc)
				return bytesToJS(buf.Bytes())
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
				buf := new(bytes.Buffer)
				io.Copy(buf, tr)
				return bytesToJS(buf.Bytes())
			}
		}
	}
	return nil
}

func compressFiles(this js.Value, args []js.Value) any {
	files := args[0]
	format := args[1].String()
	buf := new(bytes.Buffer)

	if format == "zip" {
		zw := zip.NewWriter(buf)
		keys := js.Global().Get("Object").Call("keys", files)
		for i := 0; i < keys.Length(); i++ {
			name := keys.Index(i).String()
			content := jsToBytes(files.Get(name))
			f, _ := zw.Create(name)
			f.Write(content)
		}
		zw.Close()
	} else {
		var twWriter io.WriteCloser = tar.NewWriter(buf)
		if format == "tgz" {
			gw := gzip.NewWriter(buf)
			defer gw.Close()
			twWriter = tar.NewWriter(gw)
		}
		tw := twWriter.(*tar.Writer)
		keys := js.Global().Get("Object").Call("keys", files)
		for i := 0; i < keys.Length(); i++ {
			name := keys.Index(i).String()
			content := jsToBytes(files.Get(name))
			hdr := &tar.Header{Name: name, Size: int64(len(content))}
			tw.WriteHeader(hdr)
			tw.Write(content)
		}
		tw.Close()
	}
	return bytesToJS(buf.Bytes())
}

// --- Main Entry ---

func main() {
	// This channel keeps the WASM instance alive
	c := make(chan struct{}, 0)

	// Register the functions into the JS Global scope
	js.Global().Set("listArchive", js.FuncOf(listArchive))
	js.Global().Set("compressFiles", js.FuncOf(compressFiles))
	js.Global().Set("extractFile", js.FuncOf(extractFile))

	<-c
}
