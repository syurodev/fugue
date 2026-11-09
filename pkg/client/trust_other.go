//go:build !unix

package client

import (
	"io/fs"
	"os"
)

func trustPrivate(_ fs.FileInfo, _ bool) error { return ErrUntrustedHost }

func openTrustedDiscoveryFile(_ string) (*os.File, error) { return nil, ErrUntrustedHost }
