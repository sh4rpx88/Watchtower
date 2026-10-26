package panel

import (
	"testing"
	"time"
)

func TestCronNext(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct{ expr, from, want string }{
		{"0 4 * * *", "2026-10-25 03:59", "2026-10-25 04:00"},
		{"0 4 * * *", "2026-10-25 04:00", "2026-10-26 04:00"},
		{"*/15 * * * *", "2026-10-25 10:07", "2026-10-25 10:15"},
		{"0 */6 * * *", "2026-10-25 13:30", "2026-10-25 18:00"},
		{"30 2 * * 1,5", "2026-10-25 12:00", "2026-10-26 02:30"}, // Sunday -> Monday
		{"0 0 1 * *", "2026-12-15 00:00", "2027-01-01 00:00"},
		{"0 12 * * 7", "2026-10-25 12:00", "2026-11-01 12:00"}, // 7 is Sunday
		{"0 0 13 * 5", "2026-10-25 00:00", "2026-10-30 00:00"}, // the 13th or a Friday
		{"0 0 29 2 *", "2026-03-01 00:00", "2028-02-29 00:00"},
	}
	for _, c := range cases {
		spec, err := parseCron(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if got := spec.next(at(c.from)); !got.Equal(at(c.want)) {
			t.Errorf("%s after %s = %s, want %s", c.expr, c.from, got.Format("2006-01-02 15:04"), c.want)
		}
	}
	if spec, _ := parseCron("0 0 30 2 *"); !spec.next(at("2026-01-01 00:00")).IsZero() {
		t.Error("February 30th should never run")
	}
	for _, bad := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "*/0 * * * *", "a * * * *", "5-1 * * * *"} {
		if _, err := parseCron(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
