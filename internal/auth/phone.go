package auth

import "strings"

// A phone number in one international form (+2348031234567), however it was
// typed. Drivers sign in by phone, so the number dispatch saved and the one
// the driver types must come out the same: "0803 123 4567" in Nigeria is
// "+2348031234567". The table matches the website's (lib/phone.ts).

type dialPlan struct {
	dial     string
	min, max int  // national number length, without the trunk 0
	trunk    bool // people dial a leading 0 at home and drop it from abroad
}

var dialPlans = map[string]dialPlan{
	"NG": {"234", 10, 10, true},
	"GH": {"233", 9, 9, true},
	"BJ": {"229", 8, 10, false},
	"TG": {"228", 8, 8, false},
	"CM": {"237", 9, 9, false},
	"NE": {"227", 8, 8, false},
	"CI": {"225", 10, 10, false},
	"SN": {"221", 9, 9, false},
	"KE": {"254", 9, 9, true},
	"ZA": {"27", 9, 9, true},
	"EG": {"20", 10, 10, true},
	"GB": {"44", 10, 10, true},
	"IE": {"353", 9, 9, true},
	"US": {"1", 10, 10, false},
	"AE": {"971", 9, 9, true},
	"DE": {"49", 6, 11, true},
	"FR": {"33", 9, 9, true},
	"IN": {"91", 10, 10, true},
	"CN": {"86", 11, 11, true},
}

// CanonicalPhone is NormPhone, then a number without a + is read as a
// number in country (the operator's): its trunk 0 dropped and the calling
// code added. Already-international numbers, and numbers that don't fit the
// country's length, are left as NormPhone gives them.
func CanonicalPhone(s, country string) string {
	n := NormPhone(s)
	if n == "" || strings.HasPrefix(n, "+") {
		return n
	}
	p, ok := dialPlans[strings.ToUpper(country)]
	if !ok {
		return n
	}
	national := n
	if p.trunk && strings.HasPrefix(national, "0") {
		national = national[1:]
	} else if strings.HasPrefix(n, p.dial) && len(n)-len(p.dial) >= p.min && len(n)-len(p.dial) <= p.max {
		// Typed with the calling code but no + (2348031234567).
		return "+" + n
	}
	if len(national) < p.min || len(national) > p.max {
		return n
	}
	return "+" + p.dial + national
}
