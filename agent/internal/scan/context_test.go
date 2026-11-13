package scan

import (
	"testing"
)

func TestMachineContext(t *testing.T) {
	osr := []byte("NAME=\"Ubuntu\"\nID=ubuntu\nVERSION_ID=\"22.04\"\n")
	cases := []struct {
		host   string
		labels map[string]string
		want   string
	}{
		{"12345-TN1-PRI", nil, "os=ubuntu-22.04 role=pri store=12345"},
		{"12345-tn2-sec", nil, "os=ubuntu-22.04 role=sec store=12345"},
		{"12345-TN3", nil, "os=ubuntu-22.04 role=phu store=12345"},
		{"may-la", nil, "os=ubuntu-22.04"}, // sai quy ước: không có role / store
		{"12345-TN3", map[string]string{"role": "pri", "vung": "mien-nam", "Bad": "x", "ok": "BAD"},
			"os=ubuntu-22.04 role=pri store=12345 vung=mien-nam"},
	}
	for _, c := range cases {
		if got := MachineContext(c.host, osr, c.labels).String(); got != c.want {
			t.Errorf("%s %v: %q, phải là %q", c.host, c.labels, got, c.want)
		}
	}
	if got := MachineContext("12345-TN1-PRI", []byte("ID=ubuntu\n"), nil); got["os"] != "" {
		t.Fatal("thiếu VERSION_ID thì không có os")
	}
	if got := osID([]byte("ID=ubuntu\nVERSION_ID=26.04\n")); got != "ubuntu-26.04" {
		t.Fatal(got)
	}
}
