package lumberjack

import (
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"time"
)

// A literal path component may itself contain a Go layout token, such as "2006".
// Keep literals separate from directives when formatting.
type patternPart struct {
	text  string // Literal text or the corresponding time.Layout fragment.
	code  byte
	noPad bool
}

type filePattern struct {
	parts          []patternPart
	prefix, suffix string
	parseLayout    string
	hasYear        bool
}

var layouts = map[byte]string{
	'A': "Monday", 'a': "Mon", 'B': "January", 'b': "Jan", 'h': "Jan",
	'c': time.ANSIC, 'D': "01/02/06", 'x': "01/02/06", 'd': "02", 'e': "_2",
	'F': "2006-01-02", 'H': "15", 'I': "03", 'j': "002", 'M': "04", 'm': "01",
	'p': "PM", 'R': "15:04", 'r': "03:04:05 PM", 'S': "05", 'T': "15:04:05",
	'X': "15:04:05", 'v': "_2-Jan-2006", 'Y': "2006", 'y': "06", 'Z': "MST", 'z': "-0700",
}

func compilePattern(s string) (*filePattern, error) {
	p := new(filePattern)
	var literal strings.Builder
	flushLiteral := func() {
		if literal.Len() > 0 {
			p.parts = append(p.parts, patternPart{text: literal.String()})
			literal.Reset()
		}
	}
	for len(s) > 0 {
		text, rest, found := strings.Cut(s, "%")
		literal.WriteString(text)
		if !found {
			break
		}
		s = rest
		noPad := strings.HasPrefix(s, "-") || strings.HasPrefix(s, "#")
		if noPad {
			s = s[1:]
		}
		if len(s) == 0 {
			return nil, fmt.Errorf("incomplete %% directive in PatternFileName")
		}
		code := s[0]
		s = s[1:]
		switch code {
		case '%':
			literal.WriteByte('%')
			continue
		case 'n':
			literal.WriteByte('\n')
			continue
		case 't':
			literal.WriteByte('\t')
			continue
		}
		layout := layouts[code]
		if layout == "" && strings.IndexByte("CGgklUuVWw", code) < 0 {
			return nil, fmt.Errorf("unknown directive %%%c in PatternFileName", code)
		}
		flushLiteral()
		p.parts = append(p.parts, patternPart{text: layout, code: code, noPad: noPad})
	}
	flushLiteral()
	fields := p.parts
	if len(fields) > 0 && fields[0].code == 0 {
		p.prefix = fields[0].text
		fields = fields[1:]
	}
	if len(fields) > 0 && fields[len(fields)-1].code == 0 {
		p.suffix = fields[len(fields)-1].text
		fields = fields[:len(fields)-1]
	}
	var layout strings.Builder
	for _, part := range fields {
		if part.noPad || part.text == "" {
			return p, nil
		}
		p.hasYear = p.hasYear || strings.IndexByte("YyFDxcv", part.code) >= 0
		layout.WriteString(part.text)
	}
	p.parseLayout = layout.String()
	return p, nil
}

func (p *filePattern) format(t time.Time) string {
	var buf [128]byte
	dst := buf[:0]
	for _, part := range p.parts {
		start := len(dst)
		switch {
		case part.code == 0:
			dst = append(dst, part.text...)
		case part.text != "":
			dst = t.AppendFormat(dst, part.text)
		default:
			dst = appendNumber(dst, part.code, t)
		}
		if part.noPad {
			field := dst[start:]
			if len(field) > 0 && (field[0] == '+' || field[0] == '-') {
				start++
				field = field[1:]
			}
			for len(field) > 1 && (field[0] == '0' || field[0] == ' ') {
				field = field[1:]
			}
			dst = append(dst[:start], field...)
		}
	}
	return string(dst)
}

func appendNumber(dst []byte, code byte, t time.Time) []byte {
	n, width, pad := 0, 2, byte('0')
	switch code {
	case 'C':
		n = t.Year() / 100
	case 'G', 'g', 'V':
		year, week := t.ISOWeek()
		switch code {
		case 'G':
			n, width = year, 4
		case 'g':
			n = year % 100
			if year < 0 {
				dst = append(dst, '-')
				n = -n
			}
		case 'V':
			n = week
		}
	case 'k', 'l':
		n, pad = t.Hour(), ' '
		if code == 'l' {
			n = (n+11)%12 + 1
		}
	case 'U', 'W':
		weekday := int(t.Weekday())
		if code == 'W' {
			weekday = (weekday + 6) % 7
		}
		n = (t.YearDay() + 6 - weekday) / 7
	case 'u', 'w':
		n, width = int(t.Weekday()), 1
		if code == 'u' && n == 0 {
			n = 7
		}
	}
	if n < 0 {
		dst = append(dst, '-')
		n = -n
	}
	var digits [20]byte
	value := strconv.AppendInt(digits[:0], int64(n), 10)
	for i := len(value); i < width; i++ {
		dst = append(dst, pad)
	}
	return append(dst, value...)
}

// Patterns without a recoverable date (e.g. ISO week or weekday alone) can
// still be identified by formatting the original file's modification time.
func (p *filePattern) backupTime(name string, entry fs.DirEntry) (time.Time, bool) {
	stamp, ok := strings.CutPrefix(name, p.prefix)
	if !ok || stamp == "" {
		return time.Time{}, false
	}
	stamp, ok = strings.CutSuffix(stamp, p.suffix)
	if !ok {
		return time.Time{}, false
	}
	if p.parseLayout != "" {
		t, err := time.ParseInLocation(p.parseLayout, stamp, time.Local)
		if err == nil && p.format(t) == name && p.hasYear {
			return t, true
		}
	}
	info, err := entry.Info()
	if err != nil {
		return time.Time{}, false
	}
	mtime := info.ModTime()
	return mtime, p.format(mtime.Local()) == name
}
