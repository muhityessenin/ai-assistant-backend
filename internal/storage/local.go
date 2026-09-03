package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Local struct{ root string }

func NewLocal(root string) (*Local, error) {
	r, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(r, 0750); err != nil {
		return nil, err
	}
	return &Local{root: r}, nil
}
func (l *Local) resolve(key string) (string, error) {
	if key == "" || filepath.IsAbs(key) || strings.Contains(key, "..") || filepath.Base(key) != key {
		return "", errors.New("invalid storage key")
	}
	p := filepath.Join(l.root, key)
	if !strings.HasPrefix(p, l.root+string(os.PathSeparator)) {
		return "", errors.New("invalid storage path")
	}
	return p, nil
}
func (l *Local) Save(ctx context.Context, r io.Reader, key string) (Object, error) {
	p, err := l.resolve(key)
	if err != nil {
		return Object{}, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0640)
	if err != nil {
		return Object{}, err
	}
	defer f.Close()
	n, err := copyContext(ctx, f, r)
	if err != nil {
		_ = os.Remove(p)
		return Object{}, err
	}
	return Object{Path: key, Size: n}, f.Sync()
}
func (l *Local) Open(_ context.Context, key string) (io.ReadCloser, error) {
	p, err := l.resolve(key)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}
func (l *Local) Delete(_ context.Context, key string) error {
	p, err := l.resolve(key)
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func copyContext(ctx context.Context, w io.Writer, r io.Reader) (int64, error) {
	buf := make([]byte, 64*1024)
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		m, e := r.Read(buf)
		if m > 0 {
			x, er := w.Write(buf[:m])
			n += int64(x)
			if er != nil {
				return n, er
			}
		}
		if e == io.EOF {
			return n, nil
		}
		if e != nil {
			return n, e
		}
	}
}
