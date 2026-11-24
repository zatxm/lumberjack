package lumberjack

import (
	"os"
	"syscall"
)

func chown(f *os.File, info os.FileInfo) error {
	stat := info.Sys().(*syscall.Stat_t)
	return f.Chown(int(stat.Uid), int(stat.Gid))
}
