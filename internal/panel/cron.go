package panel

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronSpec is a standard five-field cron expression: minute, hour, day of
// month, month, day of week (0 or 7 is Sunday). Fields take *, numbers,
// ranges (1-5), lists (1,15) and steps (*/15, 0-30/10).
type cronSpec struct {
	minute, hour, dom, month, dow [64]bool
	domAny, dowAny                bool // "*", which changes how day of month and day of week combine
}

var cronFields = []struct {
	name     string
	min, max int
}{{"minute", 0, 59}, {"hour", 0, 23}, {"day of month", 1, 31}, {"month", 1, 12}, {"day of week", 0, 7}}

func parseCron(expr string) (*cronSpec, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return nil, fmt.Errorf("a schedule needs 5 fields (minute hour day month weekday), got %d", len(parts))
	}
	c := &cronSpec{domAny: parts[2] == "*", dowAny: parts[4] == "*"}
	sets := []*[64]bool{&c.minute, &c.hour, &c.dom, &c.month, &c.dow}
	for i, part := range parts {
		f := cronFields[i]
		for _, item := range strings.Split(part, ",") {
			if err := setCronItem(sets[i], item, f.min, f.max); err != nil {
				return nil, fmt.Errorf("%s %q: %v", f.name, part, err)
			}
		}
	}
	if c.dow[7] {
		c.dow[0] = true
	}
	return c, nil
}

func setCronItem(set *[64]bool, item string, min, max int) error {
	step := 1
	if base, s, ok := strings.Cut(item, "/"); ok {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return fmt.Errorf("bad step %q", s)
		}
		item, step = base, n
	}
	lo, hi := min, max
	if item != "*" {
		a, b, isRange := strings.Cut(item, "-")
		var err error
		if lo, err = strconv.Atoi(a); err != nil {
			return fmt.Errorf("bad number %q", a)
		}
		hi = lo
		if isRange {
			if hi, err = strconv.Atoi(b); err != nil {
				return fmt.Errorf("bad number %q", b)
			}
		} else if step > 1 {
			hi = max // "5/10" means from 5 to the end, every 10
		}
	}
	if lo < min || hi > max || lo > hi {
		return fmt.Errorf("must be between %d and %d", min, max)
	}
	for v := lo; v <= hi; v += step {
		set[v] = true
	}
	return nil
}

// next returns the first matching minute after t, or the zero time if there
// is none within about four years (such as February 30th).
func (c *cronSpec) next(t time.Time) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	end := t.AddDate(4, 0, 1)
	for t.Before(end) {
		switch {
		case !c.month[int(t.Month())]:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
		case !c.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
		case !c.hour[t.Hour()]:
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
		case !c.minute[t.Minute()]:
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}

// dayMatches applies cron's rule: when both day of month and day of week are
// restricted, either one matching is enough.
func (c *cronSpec) dayMatches(t time.Time) bool {
	dom, dow := c.dom[t.Day()], c.dow[int(t.Weekday())]
	switch {
	case c.domAny && c.dowAny:
		return true
	case c.domAny:
		return dow
	case c.dowAny:
		return dom
	default:
		return dom || dow
	}
}
