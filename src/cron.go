package main

// Five-field cron (minute hour day-of-month month day-of-week) for bot jobs
// (botjobs.go). A schedule may hold several expressions separated by ";", so
// the Jobs tab's builder can say "7:47 AM and 9:15 AM" without being limited
// to one minute set: the next fire is the earliest of them.
//
// Fields take *, n, a-b, */s, a-b/s, a/s and comma lists; months and weekdays
// also take their English names (jan, mon). Day-of-week 7 is Sunday. When
// both day fields are restricted a day matches either one, as in Vixie cron.
// The @hourly/@daily/@weekly/@monthly/@yearly shorthands are accepted.

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	// Schedules name IANA zones; a box without /usr/share/zoneinfo (a slim
	// container) still resolves them.
	_ "time/tzdata"
)

type cronExpr struct {
	min, hour, dom, month, dow uint64
	domStar, dowStar           bool
}

type cronSchedule []cronExpr

var cronShorthands = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

var cronMonthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
var cronDayNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

// parseCronSchedule parses one or more ";"-separated expressions.
func parseCronSchedule(s string) (cronSchedule, error) {
	var out cronSchedule
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		e, err := parseCronExpr(part)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty schedule")
	}
	if len(out) > 24 {
		return nil, fmt.Errorf("at most 24 expressions in one schedule")
	}
	return out, nil
}

func parseCronExpr(s string) (cronExpr, error) {
	if long, ok := cronShorthands[strings.ToLower(s)]; ok {
		s = long
	}
	f := strings.Fields(s)
	if len(f) != 5 {
		return cronExpr{}, fmt.Errorf("%q: cron needs 5 fields (minute hour day month weekday), got %d", s, len(f))
	}
	var e cronExpr
	var err error
	if e.min, err = parseCronField(f[0], 0, 59, nil, "minute"); err != nil {
		return e, err
	}
	if e.hour, err = parseCronField(f[1], 0, 23, nil, "hour"); err != nil {
		return e, err
	}
	if e.dom, err = parseCronField(f[2], 1, 31, nil, "day of month"); err != nil {
		return e, err
	}
	if e.month, err = parseCronField(f[3], 1, 12, cronMonthNames, "month"); err != nil {
		return e, err
	}
	if e.dow, err = parseCronField(f[4], 0, 7, cronDayNames, "day of week"); err != nil {
		return e, err
	}
	// 7 is Sunday too.
	if e.dow&(1<<7) != 0 {
		e.dow = e.dow&^(1<<7) | 1
	}
	e.domStar = strings.HasPrefix(f[2], "*")
	e.dowStar = strings.HasPrefix(f[4], "*")
	return e, nil
}

func parseCronField(s string, lo, hi int, names map[string]int, what string) (uint64, error) {
	var bits uint64
	num := func(v string) (int, error) {
		if n, ok := names[strings.ToLower(v)]; ok {
			return n, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("%s: %q is not a number", what, v)
		}
		if n < lo || n > hi {
			return 0, fmt.Errorf("%s must be %d–%d, got %d", what, lo, hi, n)
		}
		return n, nil
	}
	for _, item := range strings.Split(s, ",") {
		if item == "" {
			return 0, fmt.Errorf("%s: empty list item in %q", what, s)
		}
		rng, stepStr, hasStep := strings.Cut(item, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepStr)
			if err != nil || n < 1 || n > hi {
				return 0, fmt.Errorf("%s: step %q is not usable", what, stepStr)
			}
			step = n
		}
		a, b := lo, hi
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			x, y, _ := strings.Cut(rng, "-")
			var err error
			if a, err = num(x); err != nil {
				return 0, err
			}
			if b, err = num(y); err != nil {
				return 0, err
			}
			if a > b {
				return 0, fmt.Errorf("%s: range %q runs backwards", what, rng)
			}
		default:
			n, err := num(rng)
			if err != nil {
				return 0, err
			}
			a = n
			// "5/15" is 5, 20, 35, 50: a start with a step runs to the end.
			if hasStep {
				b = hi
			} else {
				b = n
			}
		}
		for v := a; v <= b; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, nil
}

func cronHas(bits uint64, v int) bool { return bits&(1<<uint(v)) != 0 }

func (e *cronExpr) dayMatches(t time.Time) bool {
	dom, dow := cronHas(e.dom, t.Day()), cronHas(e.dow, int(t.Weekday()))
	if e.domStar || e.dowStar {
		return dom && dow
	}
	return dom || dow
}

// next is the first minute strictly after `after` that the expression
// matches, on the wall clock of loc. A time a spring-forward gap skips does
// not fire that day; one repeated by a fall-back fires once.
func (e *cronExpr) next(after time.Time, loc *time.Location) (time.Time, bool) {
	t := after.In(loc).Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		prev := t
		switch {
		case !cronHas(e.month, int(t.Month())):
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
		case !e.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
		case !cronHas(e.hour, t.Hour()):
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
		case !cronHas(e.min, t.Minute()):
			t = t.Add(time.Minute)
		case cronRepeatedWallClock(t, loc):
			// The second pass through a fall-back hour already fired.
			t = t.Add(time.Minute)
		default:
			return t, true
		}
		// A normalization across a DST change can land on or before where it
		// started; always move forward.
		if !t.After(prev) {
			t = prev.Truncate(time.Hour).Add(time.Hour)
		}
	}
	return time.Time{}, false
}

// cronRepeatedWallClock reports whether t's wall-clock time already happened
// an hour earlier, i.e. t is in the second run through a fall-back hour.
func cronRepeatedWallClock(t time.Time, loc *time.Location) bool {
	e := t.Add(-time.Hour).In(loc)
	return e.Hour() == t.Hour() && e.Minute() == t.Minute() && e.Day() == t.Day()
}

// next is the earliest next fire of any of the schedule's expressions.
func (s cronSchedule) next(after time.Time, loc *time.Location) (time.Time, bool) {
	var best time.Time
	found := false
	for i := range s {
		if t, ok := s[i].next(after, loc); ok && (!found || t.Before(best)) {
			best, found = t, true
		}
	}
	return best, found
}

// nextN lists the next n fires, for the editor's preview.
func (s cronSchedule) nextN(after time.Time, loc *time.Location, n int) []time.Time {
	var out []time.Time
	for len(out) < n {
		t, ok := s.next(after, loc)
		if !ok {
			break
		}
		out = append(out, t)
		after = t
	}
	return out
}
