// Package cron implements numeric, five-field cron expressions in an explicit timezone.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Schedule struct {
	fields [5]map[int]bool
	wild   [5]bool
	loc    *time.Location
}

func Parse(expr, zone string) (*Schedule, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 || zone == "" || zone == "Local" {
		return nil, fmt.Errorf("five cron fields and timezone required")
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, err
	}
	s := &Schedule{loc: loc}
	bounds := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for i, part := range parts {
		s.fields[i] = map[int]bool{}
		s.wild[i] = strings.HasPrefix(part, "*")
		for _, term := range strings.Split(part, ",") {
			step := 1
			pieces := strings.Split(term, "/")
			if len(pieces) > 2 {
				return nil, fmt.Errorf("invalid cron step")
			}
			if len(pieces) == 2 {
				step, err = strconv.Atoi(pieces[1])
				if err != nil || step < 1 {
					return nil, fmt.Errorf("invalid cron step")
				}
			}
			lo, hi := bounds[i][0], bounds[i][1]
			if pieces[0] != "*" {
				r := strings.Split(pieces[0], "-")
				if len(r) > 2 {
					return nil, fmt.Errorf("invalid cron range")
				}
				lo, err = strconv.Atoi(r[0])
				if err != nil {
					return nil, err
				}
				hi = lo
				if len(r) == 2 {
					hi, err = strconv.Atoi(r[1])
					if err != nil {
						return nil, err
					}
				} else if len(pieces) == 2 {
					hi = bounds[i][1]
				}
			}
			if lo < bounds[i][0] || hi > bounds[i][1] || hi < lo {
				return nil, fmt.Errorf("cron field out of range")
			}
			for n := lo; n <= hi; {
				v := n
				if i == 4 && v == 7 {
					v = 0
				}
				s.fields[i][v] = true
				if step > hi-n {
					break
				}
				n += step
			}
		}
	}
	return s, nil
}

func (s *Schedule) Matches(t time.Time) bool {
	t = t.In(s.loc)
	day, week := s.fields[2][t.Day()], s.fields[4][int(t.Weekday())]
	if !s.wild[2] && !s.wild[4] {
		day = day || week
	} else {
		day = day && week
	}
	return s.fields[0][t.Minute()] && s.fields[1][t.Hour()] && day && s.fields[3][int(t.Month())]
}
