package storage

import (
	"context"
	"io"
)

type Object struct {
	Path string
	Size int64
}
type Storage interface {
	Save(context.Context, io.Reader, string) (Object, error)
	Open(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
}
