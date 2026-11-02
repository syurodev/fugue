//go:build !linux && !darwin

package storage

import (
	"fmt"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func checkLocalFilesystem(canonicalDir string) error {
	if strings.HasPrefix(canonicalDir, `\\`) || strings.HasPrefix(canonicalDir, "//") {
		return errcode.New(
			errcode.FailedPrecondition,
			fmt.Sprintf("data directory %s is on an unsupported UNC network share", canonicalDir),
		)
	}
	return nil
}
