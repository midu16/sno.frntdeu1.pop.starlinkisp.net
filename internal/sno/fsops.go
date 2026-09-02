package sno

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// copyFile copies src to dst with the given perm.
func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.WriteFile(dst, in, perm); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}

// copyDir recursively copies src into dst, creating dst as needed.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target, 0o644)
	})
}

// jsonRoundTrip parses and re-marshals JSON, returning the canonical compact
// form matching python json.loads + json.dumps semantics.
func jsonRoundTrip(data []byte) []byte {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil
	}
	buf := bytes.NewBuffer(nil)
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil
	}
	return bytes.TrimSpace(buf.Bytes())
}

// tailBytes returns the last n bytes of the file.
func tailBytes(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	offset := int64(0)
	if info.Size() > n {
		offset = info.Size() - n
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	out := make([]byte, info.Size()-offset)
	if _, err := io.ReadFull(f, out); err != nil && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return out, nil
}

// dirEntries returns sorted names for a directory, or nil on any error.
func dirNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// fileExists reports whether path is an existing file or directory.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
