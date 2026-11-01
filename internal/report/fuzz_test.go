package report

import "testing"

func FuzzReportTextChecks(f *testing.F) {
	f.Add("GET /search?q=1'")
	f.Add("Cookie: session=abc")
	f.Add("bank card 6222020202020202")
	f.Fuzz(func(t *testing.T, text string) {
		_ = checkSensitive(map[string]string{"poc": text, "contact": text},
			map[string]string{"human_verification": text})
	})
}
