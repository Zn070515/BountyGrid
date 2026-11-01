package redact

import "testing"

func FuzzScan(f *testing.F) {
	f.Add("Cookie: session=abc")
	f.Add("Authorization: Bearer abc.def")
	f.Add("订单号 1700000000000000000")
	f.Add("银行卡 6222020202020202")
	f.Fuzz(func(t *testing.T, text string) {
		_ = Scan(text)
		_ = ScanReport(text)
		_, _ = Redact(text)
	})
}
