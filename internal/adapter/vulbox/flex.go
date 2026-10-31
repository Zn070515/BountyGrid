package vulbox

import (
	"encoding/json"
	"strconv"
	"strings"
)

// flexString accepts a JSON string or number. The Vulbox list API returns
// IDs/type/status as numbers in some responses and strings in others.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexString(s)
		return nil
	}
	s := strings.TrimSpace(string(b))
	if s == "null" {
		s = ""
	}
	*f = flexString(s)
	return nil
}

// flexInt64 accepts a JSON number or a numeric string. The Vulbox list API
// returns timestamp fields as strings and reward amounts as numbers.
type flexInt64 int64

func (f *flexInt64) UnmarshalJSON(b []byte) error {
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		v, err := n.Int64()
		if err == nil {
			*f = flexInt64(v)
			return nil
		}
	}
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return err
	}
	*f = flexInt64(v)
	return nil
}
