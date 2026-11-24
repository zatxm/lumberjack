package lumberjack

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func BenchmarkWrite(b *testing.B) {
	for _, mode := range []string{"Size", "Daily"} {
		b.Run(mode, func(b *testing.B) {
			dir := b.TempDir()
			l := &Logger{Filename: filepath.Join(dir, "app.log"), MaxSize: 1024}
			if mode == "Daily" {
				l.PatternFileName = filepath.Join(dir, "app-%Y-%m-%d.log")
			}
			defer l.Close()
			if _, err := l.Write(nil); err != nil {
				b.Fatal(err)
			}
			line := make([]byte, 128)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := l.Write(line); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkCompressEightBackups(b *testing.B) {
	dir := b.TempDir()
	l := &Logger{Filename: filepath.Join(dir, "app.log"), Compress: true}
	var paths [8]string
	for i := range paths {
		stamp := time.Unix(int64(i), 0).UTC().Format(backupTimeFormat)
		paths[i] = filepath.Join(dir, "app-"+stamp+".log")
	}
	data := make([]byte, 4096)
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		for _, path := range paths {
			if err := os.WriteFile(path, data, 0600); err != nil {
				b.Fatal(err)
			}
		}
		b.StartTimer()
		if err := l.millRunOnce(); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		for _, path := range paths {
			if err := os.Remove(path + compressSuffix); err != nil {
				b.Fatal(err)
			}
		}
		b.StartTimer()
	}
}
