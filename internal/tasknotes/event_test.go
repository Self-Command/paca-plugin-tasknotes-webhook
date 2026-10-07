package tasknotes

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestSignatureRawBytes(t *testing.T) {
	raw := []byte(`{"a": 1}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write(raw)
	sig := hex.EncodeToString(mac.Sum(nil))
	if !ValidSignature(raw, "secret", sig) || ValidSignature([]byte(`{"a":1}`), "secret", sig) || ValidSignature(raw, "wrong", sig) {
		t.Fatal("signature must authenticate exact bytes")
	}
}
func TestPrecisionAndDST(t *testing.T) {
	cases := []struct {
		s, zone, precision string
		bad                bool
	}{
		{"2026-10-07", "Asia/Shanghai", "day", false},
		{"2026-10-07T09:10", "Asia/Shanghai", "instant", false},
		{"2026-10-07T09:10:00.000", "Asia/Shanghai", "instant", false},
		{"2026-10-07T09:10:00+08:00", "Asia/Shanghai", "instant", false},
		{"2026-11-01T01:30", "America/New_York", "", true},
		{"2026-03-08T02:30", "America/New_York", "", true},
		{"", "Asia/Shanghai", "missing", false},
	}
	for _, c := range cases {
		got, err := ParseDate(c.s, c.zone)
		if (err != nil) != c.bad || (!c.bad && got.Precision != c.precision) {
			t.Fatalf("%+v: %+v %v", c, got, err)
		}
	}
}
func TestSQLDateDoesNotLoseInstantOrLocalCalendar(t *testing.T) {
	d, err := ParseDate("2026-10-07T00:10:00+08:00", "Asia/Shanghai")
	if err != nil { t.Fatal(err) }
	core, day, err := CoreDate(d,"Asia/Shanghai")
	if err != nil || core != "2026-10-07T00:00:00Z" || day != "2026-10-07" || d.Value != "2026-10-06T16:10:00Z" { t.Fatal(core,day,d.Value,err) }
	if (Task{Recurrence:""}).Recurring() || !(Task{Recurrence:"FREQ=DAILY"}).Recurring() { t.Fatal("empty recurrence is an ordinary task") }
}
