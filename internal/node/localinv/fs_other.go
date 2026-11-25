//go:build !linux

package localinv

import "os"

func hostFilesystems(_ *os.Root, _ bool) ([]Filesystem, bool) { return nil, false }
