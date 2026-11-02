// Package logtail reads the end of SMR's local log without loading huge files.
package logtail

import (
	"bytes"
	"errors"
	"io"
	"os"
)

func Tail(path string, lines int) (string, error) {
	if lines <= 0 || lines > 5000 {
		lines = 100
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	const maximum = int64(2 << 20)
	start := info.Size() - maximum
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	body, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}
	if start > 0 {
		if index := bytes.IndexByte(body, '\n'); index >= 0 {
			body = body[index+1:]
		}
	}
	parts := bytes.Split(bytes.TrimRight(body, "\n"), []byte("\n"))
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	if len(parts) == 0 {
		return "", nil
	}
	return string(bytes.Join(parts, []byte("\n"))) + "\n", nil
}

func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil || !errors.Is(err, os.ErrNotExist)
}
