//go:build !windows

package activation

import (
	"context"
	"errors"
)

// errUnsupported is every answer off Windows. The server withholds the
// block from a host that enrolled as anything else; this covers one that
// arrives anyway.
var errUnsupported = errors.New("product-key activation is not supported on this platform")

func osQuery(context.Context) (License, error) { return License{}, errUnsupported }

func osInstall(context.Context, string) error { return errUnsupported }

func osActivate(context.Context) error { return errUnsupported }
