package models

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRandomProfile(t *testing.T) {
	reDate := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	year := time.Now().UTC().Year()
	for i := 0; i < 100; i++ {
		name, bd := RandomProfile()
		if len(strings.Fields(name)) != 2 {
			t.Fatalf("name %q is not two words", name)
		}
		if !reDate.MatchString(bd) {
			t.Fatalf("birthdate %q not ISO YYYY-MM-DD", bd)
		}
		y, _ := strconv.Atoi(bd[:4])
		if y != year-25 && y > year-25 || y < year-34 {
			t.Fatalf("birth year %d outside now-25..now-34 (now=%d)", y, year)
		}
		if y < 1950 || y > 2007 {
			t.Fatalf("birth year %d outside about-you validity 1950..2007", y)
		}
		mo, _ := strconv.Atoi(bd[5:7])
		day, _ := strconv.Atoi(bd[8:10])
		if mo < 1 || mo > 12 || day < 1 || day > 28 {
			t.Fatalf("bad month/day in %q", bd)
		}
	}
}
