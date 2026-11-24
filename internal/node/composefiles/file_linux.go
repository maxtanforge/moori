package composefiles

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

const readFlags = os.O_RDONLY | syscall.O_NONBLOCK | syscall.O_NOFOLLOW

func singleLink(info fs.FileInfo) bool { return info.Sys().(*syscall.Stat_t).Nlink == 1 }

func copyOwner(file *os.File, info fs.FileInfo) error {
	dst, err := file.Stat()
	if err != nil {
		return err
	}
	from, to := info.Sys().(*syscall.Stat_t), dst.Sys().(*syscall.Stat_t)
	if from.Uid == to.Uid && from.Gid == to.Gid {
		return nil
	}
	return file.Chown(int(from.Uid), int(from.Gid))
}

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		} else {
			return err
		}
	}
}

func copyAccessACL(dir *os.Root, name string, dst *os.File, expected fs.FileInfo) error {
	source, err := dir.OpenFile(name, readFlags, 0)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !os.SameFile(expected, info) {
		return ErrConflict
	}
	const acl = "system.posix_acl_access"
	size, err := unix.Fgetxattr(int(source.Fd()), acl, nil)
	switch {
	case errors.Is(err, unix.ENODATA):
		// A restrictive source must not gain ACL entries inherited by the temporary file.
		err = unix.Fremovexattr(int(dst.Fd()), acl)
		if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) {
			return nil
		}
		return err
	case errors.Is(err, unix.ENOTSUP):
		return nil
	case err != nil:
		return err
	}
	if size <= 0 || size > 64<<10 {
		return ErrInvalidSource
	}
	data := make([]byte, size)
	n, err := unix.Fgetxattr(int(source.Fd()), acl, data)
	if err != nil {
		return err
	}
	return unix.Fsetxattr(int(dst.Fd()), acl, data[:n], 0)
}
