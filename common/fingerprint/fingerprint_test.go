package fingerprint

import (
	"net/netip"
	"reflect"
	"testing"
)

var sample = Components{
	MachineID:    "4c4c4544004c3510804bc4c04f4a4d32",
	BoardSerial:  "PF2ABCDE",
	SystemSerial: "SN123456",
	PrimaryMAC:   "00:1A:2B:3C:4D:5E",
}

// Test vector cố định: agent và server phải ra đúng các giá trị này.
// Nếu đổi thuật toán thì phải tăng context (scs-fp/v2), không sửa vector.
const (
	sampleFP   = "3aad75c8b5232d6ac67ffd4a8ef4bfa42d9df7421d9d323860b7680aa83dd41c"
	sampleSite = "cb4ea22dfd3169e0c0493c2f42c14bb02f1bd8e128af93605666a8973b9208b4"
)

func TestHashVector(t *testing.T) {
	if got := sample.Hash(); got != sampleFP {
		t.Fatalf("Hash() = %s, muốn %s", got, sampleFP)
	}
}

func TestSiteKeyVector(t *testing.T) {
	ip := netip.MustParseAddr("203.0.113.10")
	if got := SiteKey(sampleFP, ip); got != sampleSite {
		t.Fatalf("SiteKey() = %s, muốn %s", got, sampleSite)
	}
	mapped := netip.MustParseAddr("::ffff:203.0.113.10")
	if got := SiteKey(sampleFP, mapped); got != sampleSite {
		t.Fatalf("IPv4-mapped phải cho cùng site_key, được %s", got)
	}
}

func TestNormalizeStable(t *testing.T) {
	messy := Components{
		MachineID:    "  4C4C4544004C3510804BC4C04F4A4D32\n",
		BoardSerial:  " pf2abcde ",
		SystemSerial: "sn123456",
		PrimaryMAC:   "00-1a-2b-3c-4d-5e",
	}
	if messy.Hash() != sample.Hash() {
		t.Fatal("cùng máy nhưng định dạng khác phải ra cùng fingerprint")
	}
}

func TestPlaceholderSerials(t *testing.T) {
	for _, s := range []string{"To be filled by O.E.M.", "Default string", "0", " None ", "System Serial Number"} {
		c := Components{BoardSerial: s}.Normalize()
		if c.BoardSerial != "" {
			t.Errorf("%q phải coi là không có serial, được %q", s, c.BoardSerial)
		}
	}
}

func TestFieldSeparation(t *testing.T) {
	a := Components{MachineID: "ab", BoardSerial: "c"}
	b := Components{MachineID: "a", BoardSerial: "bc"}
	if a.Hash() == b.Hash() {
		t.Fatal("ranh giới giữa các trường phải ảnh hưởng tới hash")
	}
}

func TestDiff(t *testing.T) {
	if d := Diff(sample, sample); len(d) != 0 {
		t.Fatalf("không đổi mà Diff = %v", d)
	}
	nic := sample
	nic.PrimaryMAC = "00:1a:2b:3c:4d:ff"
	if d := Diff(sample, nic); !reflect.DeepEqual(d, []string{"primary_mac"}) {
		t.Fatalf("thay card mạng: Diff = %v", d)
	}
	clone := nic
	clone.MachineID = "ffffffffffffffffffffffffffffffff"
	if d := Diff(sample, clone); len(d) != 2 {
		t.Fatalf("đổi 2 thành phần: Diff = %v", d)
	}
}
