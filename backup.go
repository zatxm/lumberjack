package lumberjack

import (
	"cmp"
	"compress/gzip"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Called with l.mu held. Coalesce rotations during a scan and let the worker
// exit when caught up, even if the caller never closes the logger.
func (l *Logger) mill() {
	if !l.Compress && l.MaxAge <= 0 && l.MaxBackups <= 0 {
		return
	}
	if l.millDone != nil {
		l.millAgain = true
		return
	}
	l.millDone = make(chan struct{})
	go func() {
		for {
			err := l.millRunOnce()
			l.mu.Lock()
			if l.millErr == nil {
				l.millErr = err
			}
			if !l.millAgain {
				close(l.millDone)
				l.millDone = nil
				l.mu.Unlock()
				return
			}
			l.millAgain = false
			l.mu.Unlock()
		}
	}()
}

type logInfo struct {
	path      string // Full path including any compression suffix.
	base      string // Uncompressed name for counting logical backups.
	timestamp time.Time
}

func (l *Logger) millRunOnce() error {
	files, err := l.oldLogFiles()
	if err != nil {
		return err
	}
	var kept map[string]bool
	if l.MaxBackups > 0 && len(files) > l.MaxBackups {
		slices.SortFunc(files, func(a, b logInfo) int {
			if c := b.timestamp.Compare(a.timestamp); c != 0 {
				return c
			}
			return cmp.Compare(a.base, b.base)
		})
		kept = make(map[string]bool, l.MaxBackups)
	}
	var cutoff time.Time
	if days := int64(l.MaxAge); days > 0 && days <= math.MaxInt64/86400 {
		now := currentTime()
		cutoff = time.Unix(now.Unix()-days*86400, int64(now.Nanosecond()))
	}
	var gz *gzip.Writer
	for _, f := range files {
		remove := !cutoff.IsZero() && f.timestamp.Before(cutoff)
		if !remove && kept != nil && !kept[f.base] {
			if len(kept) >= l.MaxBackups {
				remove = true
			} else {
				kept[f.base] = true
			}
		}
		var fileErr error
		if remove {
			fileErr = os.Remove(f.path)
		} else if l.Compress && f.base == filepath.Base(f.path) {
			if gz == nil {
				gz = gzip.NewWriter(io.Discard)
			}
			fileErr = compressLogFile(f.path, gz)
		}
		if err == nil {
			err = fileErr
		}
	}
	return err
}

// Only regular files in the active log directory are eligible for retention.
func (l *Logger) oldLogFiles() ([]logInfo, error) {
	name, err := filepath.Abs(l.filename())
	if err != nil {
		return nil, err
	}
	dir, active := filepath.Split(name)
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	ext := filepath.Ext(active)
	prefix := strings.TrimSuffix(active, ext) + "-"
	loc := time.UTC
	if l.LocalTime {
		loc = time.Local
	}
	backupTime := func(base string, entry os.DirEntry) (time.Time, bool) {
		if stamp, ok := strings.CutPrefix(base, prefix); ok {
			if stamp, ok = strings.CutSuffix(stamp, ext); ok && len(stamp) == len(backupTimeFormat) {
				if t, err := time.ParseInLocation(backupTimeFormat, stamp, loc); err == nil {
					return t, true
				}
			}
		}
		if l.pattern != nil {
			return l.pattern.backupTime(filepath.Join(dir, base), entry)
		}
		return time.Time{}, false
	}
	var backups []logInfo
	for _, f := range files {
		if !f.Type().IsRegular() || f.Name() == active {
			continue
		}
		base := f.Name()
		t, ok := backupTime(base, f)
		if !ok {
			base, ok = strings.CutSuffix(base, compressSuffix)
			if !ok {
				continue
			}
			t, ok = backupTime(base, f)
			if !ok {
				continue
			}
		}
		backups = append(backups, logInfo{filepath.Join(dir, f.Name()), base, t})
	}
	return backups, nil
}

func compressLogFile(src string, gz *gzip.Writer) error {
	dst := src + compressSuffix
	// A pre-existing pair may contain different data. Preserve both copies.
	if existing, err := os.Lstat(dst); err == nil {
		if !existing.Mode().IsRegular() {
			return fmt.Errorf("compressed log destination %q is not a regular file", dst)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(src), ".lumberjack-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := copyModeOwner(tmp, info); err != nil {
		return err
	}
	gz.Reset(tmp)
	if _, err := io.Copy(gz, f); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chtimes(tmp.Name(), info.ModTime(), info.ModTime()); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return fmt.Errorf("publish compressed log: %w", err)
	}
	return os.Remove(src)
}
