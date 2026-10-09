package rulebus_test

import (
	"testing"

	"github.com/jroedel/reconcile/business/domain/rule/rulebus"
	"github.com/jroedel/reconcile/business/types"
)

func TestSuggestions(t *testing.T) {
	fuel, food, flowers := types.NewID(), types.NewID(), types.NewID()

	s := rulebus.Learn([]rulebus.Example{
		{"SHELL OIL 57444 SPRINGFIELD", fuel},
		{"SHELL OIL 57444 SPRINGFIELD", fuel},
		{"POS 0712 SHELL OIL 12", fuel},
		{"JOES PIZZA #12", food},
		{"JOES PIZZA #13", food},
		{"CORNER GROCERY", food},
		{"CORNER GROCERY", flowers},
		{"ROSE FLORIST", flowers},
		{"MAPLE CAFE", food},
		{"MAPLE CAFE", food},
		{"MAPLE CAFE", food},
		{"MAPLE CAFE", food},
		{"MAPLE CAFE", flowers},
		{"NOT SORTED", types.ID{}},
	})

	for _, c := range []struct {
		description string
		want        types.ID
		method      rulebus.Method
		agree, of   int
	}{
		{"SHELL OIL 60100 CAPITAL", fuel, rulebus.SamePayee, 3, 3},
		{"SQ *JOE'S PIZZA", food, rulebus.SimilarName, 2, 2}, // a name nearly the same
		{"MAPLE CAFE", food, rulebus.SamePayee, 4, 5},        // four in five
		{"CORNER GROCERY", types.ID{}, "", 0, 0},             // half and half says nothing
		{"ROSE FLORIST", types.ID{}, "", 0, 0},               // one example is too few
		{"NOT SORTED", types.ID{}, "", 0, 0},                 // not sorted teaches nothing
		{"ELECTRIC CO", types.ID{}, "", 0, 0},                // nothing like it
	} {
		got, ok := s.Suggest(c.description)

		switch {
		case c.want.Zero() && ok:
			t.Errorf("%s: suggested %+v", c.description, got)
		case !c.want.Zero() && (!ok || got.CategoryID != c.want || got.Method != c.method || got.Agree != c.agree || got.Of != c.of):
			t.Errorf("%s: %+v %v", c.description, got, ok)
		}
	}
}
