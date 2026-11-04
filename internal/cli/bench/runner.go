package bench

import (
	"context"
	"io"
)

type remoteRunner interface {
	Run(ctx context.Context, command string, stdout, stderr io.Writer) error
}
