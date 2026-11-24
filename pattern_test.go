package lumberjack

import (
	"testing"
	"time"
)

func TestPattern(t *testing.T) {
	tm := time.Date(2021, 1, 3, 4, 5, 9, 0, time.FixedZone("NST", -3*3600-1800))
	for _, tc := range []struct{ pattern, want string }{
		{"2006/Jan/MST/01-%%-%Y%m%d", "2006/Jan/MST/01-%-20210103"},
		{"%A %a %B %b %h %C %c", "Sunday Sun January Jan Jan 20 Sun Jan  3 04:05:09 2021"},
		{"%D %d %e %F %G %g", "01/03/21 03  3 2021-01-03 2020 20"},
		{"%H %I %j %k %l %M %m %n%p", "04 04 003  4  4 05 01 \nAM"},
		{"%R %r %S %T%t%U %u %V %v %W %w", "04:05 04:05:09 AM 09 04:05:09\t01 7 53  3-Jan-2021 00 0"},
		{"%X %x %Y %y %Z %z %%", "04:05:09 01/03/21 2021 21 NST -0330 %"},
		{"%-d %#m %-j %#k %-l %#z %-A %#x", "3 1 3 4 4 -330 Sunday 1/03/21"},
	} {
		p, err := compilePattern(tc.pattern)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.format(tm); got != tc.want {
			t.Errorf("%q = %q, want %q", tc.pattern, got, tc.want)
		}
	}
	for _, invalid := range []string{"%", "bad%#", "%-", "%Q", "%f"} {
		if _, err := compilePattern(invalid); err == nil {
			t.Errorf("accepted invalid pattern %q", invalid)
		}
	}
}
