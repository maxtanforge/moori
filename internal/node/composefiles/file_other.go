//go:build !linux

package composefiles

import (
	"io/fs"
	"os"
	"os/exec"
)

const readFlags = os.O_RDONLY

func singleLink(fs.FileInfo) bool           { return true }
func copyOwner(*os.File, fs.FileInfo) error { return nil }

func configureProcess(*exec.Cmd) {}

func copyAccessACL(*os.Root, string, *os.File, fs.FileInfo) error { return nil }
