package limiter

import (
	"encoding/json"
	"fmt"
	"time"
)

// Rules are stored/served as JSON with a human-friendly window ("1m", "30s") instead of nanoseconds.
type ruleJSON struct {
	Algorithm Algorithm `json:"algorithm"`
	Limit     int64     `json:"limit"`
	Window    string    `json:"window"`
	Burst     int64     `json:"burst,omitempty"`
}

func (r Rule) MarshalJSON() ([]byte, error) {
	return json.Marshal(ruleJSON{r.Algorithm, r.Limit, r.Window.String(), r.Burst})
}

func (r *Rule) UnmarshalJSON(b []byte) error {
	var j ruleJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	d, err := time.ParseDuration(j.Window)
	if err != nil {
		return fmt.Errorf("%w: window %q: %v", ErrInvalidRule, j.Window, err)
	}
	*r = Rule{Algorithm: j.Algorithm, Limit: j.Limit, Window: d, Burst: j.Burst}
	return nil
}
