package decision

import "testing"

type fixedPolicy map[string]bool

func (f fixedPolicy) FailOpen(tenant string) (bool, bool) { v, ok := f[tenant]; return v, ok }

func TestShouldFailOpenPrefersTenantPolicy(t *testing.T) {
	c := Checker{Policy: fixedPolicy{"strict": false, "lenient": true}}
	for _, tc := range []struct {
		tenant string
		global bool
		want   bool
	}{
		{"strict", true, false},  // tenant says closed, global says open
		{"lenient", false, true}, // tenant says open, global says closed
		{"other", true, true},    // no override: global applies
		{"other", false, false},
	} {
		if got := c.ShouldFailOpen(tc.tenant, tc.global); got != tc.want {
			t.Errorf("%s global=%v: got %v want %v", tc.tenant, tc.global, got, tc.want)
		}
	}
	if !(Checker{}).ShouldFailOpen("x", true) {
		t.Error("no policy configured: the global default applies")
	}
}
