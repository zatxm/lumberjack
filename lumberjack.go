package lumberjack

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	backupTimeFormat = "2006-01-02T15-04-05.000"
	compressSuffix   = ".gz"
	defaultMaxSize   = 100
)

// ensure we always implement io.WriteCloser
var _ io.WriteCloser = (*Logger)(nil)

// Logger writes to one active file. Configure it before use and use one writer
// per file. Write, Rotate and Close may be called concurrently.
type Logger struct {
	// Filename is the file to write logs to.  Backup log files will be retained
	// in the same directory.  It uses <processname>-lumberjack.log in
	// os.TempDir() if empty.
	Filename string `json:"filename" yaml:"filename"`

	// MaxSize is the maximum size in megabytes of the log file before it gets
	// rotated. It defaults to 100 megabytes.
	MaxSize int `json:"maxsize" yaml:"maxsize"`

	// MaxAge is the maximum number of days to retain old log files based on the
	// timestamp encoded in their filename.  Note that a day is defined as 24
	// hours and may not exactly correspond to calendar days due to daylight
	// savings, leap seconds, etc. The default is not to remove old log files
	// based on age.
	MaxAge int `json:"maxage" yaml:"maxage"`

	// MaxBackups is the maximum number of old log files to retain.  The default
	// is to retain all old log files (though MaxAge may still cause them to get
	// deleted.)
	MaxBackups int `json:"maxbackups" yaml:"maxbackups"`

	// LocalTime determines if the time used for formatting the timestamps in
	// backup files is the computer's local time.  The default is to use UTC
	// time.
	LocalTime bool `json:"localtime" yaml:"localtime"`

	// Compress determines if the rotated log files should be compressed
	// using gzip. The default is not to perform compression.
	Compress bool `json:"compress" yaml:"compress"`

	// PatternFileName names backups using local time and strftime directives.
	// When set, changes in the formatted name trigger rotation instead of MaxSize.
	PatternFileName string `json:"patternfilename" yaml:"patternfilename"`

	mu       sync.Mutex
	file     *os.File
	fileInfo os.FileInfo // Identity of the open descriptor, for os.SameFile.
	pattern  *filePattern
	period   string // Archive name for data in the active file.

	cachedSecond int64
	cachedName   string

	millDone  chan struct{}
	millAgain bool
	millErr   error
}

var currentTime = time.Now

// Write implements io.Writer.
func (l *Logger) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := l.preparePattern(); err != nil {
		return 0, err
	}
	maxSize := l.max()
	if l.pattern == nil && int64(len(p)) > maxSize {
		return 0, fmt.Errorf("write length %d exceeds maximum file size %d", len(p), maxSize)
	}
	info, err := l.activeFile()
	if err != nil {
		return 0, err
	}
	if l.pattern != nil {
		now := currentTime()
		if l.cachedName == "" || l.cachedSecond != now.Unix() {
			l.cachedName = l.pattern.format(now)
			l.cachedSecond = now.Unix()
		}
		if info.Size() > 0 && l.period != "" && l.period != l.cachedName {
			if err := l.rotate(l.period); err != nil {
				return 0, err
			}
		}
		l.period = l.cachedName
	} else if info.Size() > maxSize-int64(len(p)) {
		if err := l.rotate(""); err != nil {
			return 0, err
		}
	}
	return l.file.Write(p)
}

func (l *Logger) preparePattern() error {
	if l.PatternFileName == "" || l.pattern != nil {
		return nil
	}
	pattern := l.PatternFileName
	if !filepath.IsAbs(pattern) {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		pattern = filepath.Join(strings.ReplaceAll(cwd, "%", "%%"), pattern)
	}
	var err error
	l.pattern, err = compilePattern(filepath.Clean(pattern))
	return err
}

// Close closes the active file, waits for scheduled maintenance and reports
// the first maintenance error since the previous Close.
// A subsequent Write reopens the file.
func (l *Logger) Close() error {
	l.mu.Lock()
	err := l.closeFile()
	done := l.millDone
	l.mu.Unlock()
	if done != nil {
		<-done
	}
	l.mu.Lock()
	err = errors.Join(err, l.millErr)
	l.millErr = nil
	l.mu.Unlock()
	return err
}

func (l *Logger) closeFile() error {
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file, l.fileInfo = nil, nil
	return err
}

// Rotate archives the current file under a timestamped name, opens a new
// active file and schedules compression and removal of old backups.
func (l *Logger) Rotate() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.preparePattern(); err != nil {
		return err
	}
	return l.rotate("")
}

func (l *Logger) rotate(archive string) error {
	name := l.filename()
	info, err := os.Stat(name)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var dest string
	if info != nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("cannot rotate non-regular log file %q", name)
		}
		dest, err = l.archiveName(name, archive)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return err
		}
	}
	if err := l.closeFile(); err != nil {
		return err
	}
	if dest != "" {
		if err := os.Rename(name, dest); err != nil {
			return fmt.Errorf("archive log: %w", err)
		}
	}
	_, err = l.open(name, info)
	return err
}

// An earlier rotation, including its compressed copy, owns its name. On a
// collision use a standard timestamped backup that retention also recognizes.
func (l *Logger) archiveName(name, archive string) (string, error) {
	t := currentTime()
	if !l.LocalTime {
		t = t.UTC()
	}
	ext := filepath.Ext(name)
	prefix := strings.TrimSuffix(name, ext) + "-"
	for {
		if archive == "" {
			archive = prefix + t.Format(backupTimeFormat) + ext
			t = t.Add(time.Millisecond)
		}
		_, err := os.Lstat(archive)
		if os.IsNotExist(err) {
			_, err = os.Lstat(archive + compressSuffix)
			if os.IsNotExist(err) {
				return archive, nil
			}
		}
		if err != nil {
			return "", err
		}
		archive = ""
	}
}

// Check the path on every write so external replacement or truncation takes
// effect immediately. O_APPEND already accounts for changes in file length.
func (l *Logger) activeFile() (os.FileInfo, error) {
	if l.file != nil {
		info, err := os.Stat(l.file.Name())
		if err == nil && os.SameFile(l.fileInfo, info) {
			return info, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err := l.closeFile(); err != nil {
			return nil, err
		}
	}
	return l.open(l.filename(), nil)
}

// previous supplies the mode and owner of a file we have just archived.
func (l *Logger) open(name string, previous os.FileInfo) (os.FileInfo, error) {
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		return nil, err
	}
	const flags = os.O_APPEND | os.O_WRONLY | os.O_CREATE
	var f *os.File
	var err error
	if previous != nil {
		f, err = os.OpenFile(name, flags|os.O_EXCL, previous.Mode().Perm())
		if err == nil {
			if err := copyModeOwner(f, previous); err != nil {
				f.Close()
				return nil, err
			}
		} else if !os.IsExist(err) {
			return nil, err
		}
	}
	if f == nil {
		f, err = os.OpenFile(name, flags, 0600)
		if err != nil {
			return nil, err
		}
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	l.file, l.fileInfo = f, info
	l.period = ""
	if l.pattern != nil && info.Size() > 0 {
		l.period = l.pattern.format(info.ModTime().Local())
	}
	l.mill()
	return info, nil
}

func (l *Logger) filename() string {
	if l.Filename != "" {
		return l.Filename
	}
	return filepath.Join(os.TempDir(), filepath.Base(os.Args[0])+"-lumberjack.log")
}

func (l *Logger) max() int64 {
	size := l.MaxSize
	if size == 0 {
		size = defaultMaxSize
	}
	const megabyte = 1024 * 1024
	if int64(size) > math.MaxInt64/megabyte {
		return math.MaxInt64
	}
	if size < 0 {
		return -1
	}
	return int64(size) * megabyte
}

// Apply the archived file's permissions even when the process umask differs.
func copyModeOwner(f *os.File, info os.FileInfo) error {
	if err := chown(f, info); err != nil {
		return err
	}
	return f.Chmod(info.Mode().Perm())
}
