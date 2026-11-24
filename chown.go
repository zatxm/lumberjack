//go:build !linux

package lumberjack

import "os"

func chown(_ *os.File, _ os.FileInfo) error {
	return nil
}
