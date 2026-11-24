package lumberjack

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func clockAt(t *testing.T, tm time.Time) func(time.Time) {
	t.Helper()
	var clock atomic.Int64
	set := func(v time.Time) { clock.Store(v.UnixNano()) }
	set(tm)
	old := currentTime
	currentTime = func() time.Time { return time.Unix(0, clock.Load()).Local() }
	t.Cleanup(func() { currentTime = old })
	return set
}

func writeLog(t *testing.T, l *Logger, text string) {
	t.Helper()
	if n, err := l.Write([]byte(text)); err != nil || n != len(text) {
		t.Fatalf("Write = %d, %v", n, err)
	}
}

func putFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func contents(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(path, ".gz") {
		z, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer z.Close()
		b, err = io.ReadAll(z)
		if err != nil {
			t.Fatal(err)
		}
	}
	return string(b)
}

func TestExternalTruncate(t *testing.T) {
	name := filepath.Join(t.TempDir(), "app.log")
	l := &Logger{Filename: name}
	defer l.Close()
	writeLog(t, l, "first")
	if err := os.Truncate(name, 0); err != nil {
		t.Fatal(err)
	}
	writeLog(t, l, "second")
	if got := contents(t, name); got != "second" {
		t.Fatal(got)
	}
}

func TestExternalReplaceAndRemove(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows denies external renames of files opened with os.OpenFile")
	}
	name := filepath.Join(t.TempDir(), "app.log")
	l := &Logger{Filename: name}
	defer l.Close()
	writeLog(t, l, "original")
	if err := os.Rename(name, name+".old"); err != nil {
		t.Fatal(err)
	}
	putFile(t, name, "external")
	writeLog(t, l, " third")
	if got := contents(t, name); got != "external third" {
		t.Fatal(got)
	}
	if got := contents(t, name+".old"); got != "original" {
		t.Fatal(got)
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	writeLog(t, l, "fourth")
	if got := contents(t, name); got != "fourth" {
		t.Fatal(got)
	}
}

func TestSizeBoundaryAndBackupCollision(t *testing.T) {
	now := time.Date(2024, 2, 29, 12, 0, 0, 0, time.UTC)
	clockAt(t, now)
	dir := t.TempDir()
	name := filepath.Join(dir, "app.log")
	l := &Logger{Filename: name, MaxSize: 1}
	defer l.Close()
	putFile(t, name, strings.Repeat("x", (1<<20)-1))
	writeLog(t, l, "a")
	stamp := func(i int) string {
		return filepath.Join(dir, "app-"+now.Add(time.Duration(i)*time.Millisecond).Format(backupTimeFormat)+".log")
	}
	putFile(t, stamp(0)+".gz", "existing compressed backup")
	writeLog(t, l, "b")
	if data := contents(t, stamp(1)); len(data) != 1<<20 || data[len(data)-1] != 'a' {
		t.Fatal("rotation lost the write that exactly fit")
	}
	if err := l.Rotate(); err != nil {
		t.Fatal(err)
	}
	if got := contents(t, stamp(2)); got != "b" {
		t.Fatal(got)
	}
	if n, err := l.Write(make([]byte, (1<<20)+1)); n != 0 || err == nil {
		t.Fatalf("oversized write = %d, %v", n, err)
	}
}

func TestPatternRolloverAndRestart(t *testing.T) {
	start := time.Date(2024, 2, 28, 23, 59, 59, 0, time.Local)
	set := clockAt(t, start)
	dir := t.TempDir()
	name := filepath.Join(dir, "app.log")
	pattern := filepath.Join(dir, "daily-%F.log")
	l := &Logger{Filename: name, PatternFileName: pattern}
	defer l.Close()
	writeLog(t, l, "before midnight")
	set(start.Add(time.Second))
	writeLog(t, l, "leap day")
	if got := contents(t, filepath.Join(dir, "daily-2024-02-28.log")); got != "before midnight" {
		t.Fatal(got)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	mtime := start.Add(time.Hour)
	if err := os.Chtimes(name, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	set(start.AddDate(0, 0, 2))
	restarted := &Logger{Filename: name, PatternFileName: pattern}
	defer restarted.Close()
	writeLog(t, restarted, "march")
	if got := contents(t, filepath.Join(dir, "daily-2024-02-29.log")); got != "leap day" {
		t.Fatal(got)
	}
	if got := contents(t, name); got != "march" {
		t.Fatal(got)
	}
}

func TestInvalidPattern(t *testing.T) {
	name := filepath.Join(t.TempDir(), "app.log")
	putFile(t, name, "original")
	l := &Logger{Filename: name, PatternFileName: "%Q"}
	if n, err := l.Write([]byte("new")); err == nil || n != 0 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if err := l.Rotate(); err == nil {
		t.Fatal("invalid pattern accepted by Rotate")
	}
	if got := contents(t, name); got != "original" {
		t.Fatal(got)
	}
}

func TestRetention(t *testing.T) {
	now := time.Date(2024, 3, 10, 12, 0, 0, 0, time.Local)
	clockAt(t, now)
	dir := t.TempDir()
	l := &Logger{
		Filename: filepath.Join(dir, "app.log"), PatternFileName: filepath.Join(dir, "daily-%%-%F.log"),
		MaxBackups: 2, MaxAge: 7, Compress: true, LocalTime: true,
	}
	defer l.Close()
	for _, name := range []string{
		"app-2024-03-10T11-00-00.000.log", "daily-%-2024-03-09.log", "daily-%-2024-03-08.log",
		"daily-%-2024-03-01.log.gz", "daily-%-2024-02-30.log", "app-unrelated.log", "app-.log",
	} {
		putFile(t, filepath.Join(dir, name), name)
	}
	writeLog(t, l, "active")
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app-2024-03-10T11-00-00.000.log", "daily-%-2024-03-09.log"} {
		if got := contents(t, filepath.Join(dir, name+".gz")); got != name {
			t.Fatal(got)
		}
	}
	for _, name := range []string{"daily-%-2024-03-08.log", "daily-%-2024-03-01.log.gz"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s should have expired, got %v", name, err)
		}
	}
	for _, name := range []string{"daily-%-2024-02-30.log", "app-unrelated.log", "app-.log"} {
		if got := contents(t, filepath.Join(dir, name)); got != name {
			t.Fatal(got)
		}
	}
}

func TestConcurrentWriteRotateClose(t *testing.T) {
	dir := t.TempDir()
	l := &Logger{Filename: filepath.Join(dir, "app.log"), Compress: true}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 50 {
				if _, err := l.Write([]byte("entry\n")); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Go(func() {
		for range 20 {
			if err := l.Rotate(); err != nil {
				t.Error(err)
				return
			}
			if err := l.Close(); err != nil {
				t.Error(err)
				return
			}
		}
	})
	wg.Wait()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, f := range files {
		all.WriteString(contents(t, filepath.Join(dir, f.Name())))
	}
	if all.String() != strings.Repeat("entry\n", 200) {
		t.Fatal("lost or damaged log records")
	}
}

func TestRetryFailedRollover(t *testing.T) {
	start := time.Date(2024, 12, 31, 23, 59, 59, 0, time.Local)
	set := clockAt(t, start)
	dir := t.TempDir()
	archiveDir := filepath.Join(dir, "archives")
	putFile(t, archiveDir, "blocks creation of archive directory")
	l := &Logger{Filename: filepath.Join(dir, "app.log"), PatternFileName: filepath.Join(archiveDir, "app-%F.log")}
	defer l.Close()
	writeLog(t, l, "december")
	set(start.Add(time.Second))
	if n, err := l.Write([]byte("january")); n != 0 || err == nil {
		t.Fatalf("blocked rollover = %d, %v", n, err)
	}
	if got := contents(t, l.Filename); got != "december" {
		t.Fatal(got)
	}
	if err := os.Remove(archiveDir); err != nil {
		t.Fatal(err)
	}
	writeLog(t, l, "january")
	if got := contents(t, filepath.Join(archiveDir, "app-2024-12-31.log")); got != "december" {
		t.Fatal(got)
	}
	if got := contents(t, l.Filename); got != "january" {
		t.Fatal(got)
	}
}

func TestAgeCutoffAndCompressedPair(t *testing.T) {
	now := time.Date(2024, 4, 3, 12, 0, 0, 0, time.UTC)
	clockAt(t, now)
	dir := t.TempDir()
	old := filepath.Join(dir, "app-2024-04-02T11-59-59.999.log")
	boundary := filepath.Join(dir, "app-2024-04-02T12-00-00.000.log")
	newest := filepath.Join(dir, "app-2024-04-03T11-00-00.000.log")
	putFile(t, old, "expired")
	putFile(t, boundary, "exact cutoff")
	putFile(t, newest, "raw")
	putFile(t, newest+".gz", "pre-existing content")
	l := &Logger{Filename: filepath.Join(dir, "app.log"), MaxAge: 1, MaxBackups: 2, Compress: true}
	writeLog(t, l, "active")
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("expired backup still present: %v", err)
	}
	if got := contents(t, boundary+".gz"); got != "exact cutoff" {
		t.Fatal(got)
	}
	if got := contents(t, newest); got != "raw" {
		t.Fatal(got)
	}
	if b, err := os.ReadFile(newest + ".gz"); err != nil || string(b) != "pre-existing content" {
		t.Fatalf("pre-existing backup = %q, %v", b, err)
	}
}

func TestCompressionErrorAndRetry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app-2024-05-01T11-00-00.000.log")
	putFile(t, path, "archive")
	if err := os.Mkdir(path+".gz", 0700); err != nil {
		t.Fatal(err)
	}
	l := &Logger{Filename: filepath.Join(dir, "app.log"), Compress: true}
	writeLog(t, l, "first")
	if err := l.Close(); err == nil {
		t.Fatal("Close did not report the blocked compression destination")
	}
	if got := contents(t, path); got != "archive" {
		t.Fatal(got)
	}
	if err := os.Remove(path + ".gz"); err != nil {
		t.Fatal(err)
	}
	writeLog(t, l, "second")
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if got := contents(t, path+".gz"); got != "archive" {
		t.Fatal(got)
	}
}

func TestWeekPatternPreservesTimeAcrossCompression(t *testing.T) {
	stamp := time.Date(2021, 1, 3, 12, 0, 0, 0, time.Local)
	clockAt(t, stamp.AddDate(0, 0, 5))
	dir := t.TempDir()
	path := filepath.Join(dir, "week-2020-W53.log")
	putFile(t, path, "new year")
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	pattern := filepath.Join(dir, "week-%G-W%V.log")
	name := filepath.Join(dir, "app.log")
	l := &Logger{Filename: name, PatternFileName: pattern, Compress: true}
	writeLog(t, l, "first")
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if got := contents(t, path+".gz"); got != "new year" {
		t.Fatal(got)
	}
	restarted := &Logger{Filename: name, PatternFileName: pattern, MaxAge: 4}
	writeLog(t, restarted, "second")
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".gz"); !os.IsNotExist(err) {
		t.Fatalf("expired week archive still present: %v", err)
	}
}
