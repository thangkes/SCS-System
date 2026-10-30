package protocol

import (
	"reflect"
	"testing"
)

func TestCheckServerURL(t *testing.T) {
	for _, s := range []string{"https://192.168.1.10:6222", "https://ten.ddns.net:16222/", "https://[fd00::1]:6222"} {
		if err := CheckServerURL(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []string{"", "http://1.2.3.4:6222", "https://1.2.3.4", "https://1.2.3.4:6222/x", "https://u@1.2.3.4:1", "https://1.2.3.4:1?a=b"} {
		if CheckServerURL(s) == nil {
			t.Errorf("%q phải bị từ chối", s)
		}
	}
}

func TestCleanServers(t *testing.T) {
	got := CleanServers([]string{" https://a.b:1/ ", "https://a.b:1", "http://x:1", "https://c.d:2"})
	want := []string{"https://a.b:1", "https://c.d:2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	var many []string
	for i := range 20 {
		many = append(many, "https://h"+string(rune('a'+i))+".x:1")
	}
	if n := len(CleanServers(many)); n != MaxServers {
		t.Fatalf("giữ %d địa chỉ, phải là %d", n, MaxServers)
	}
}
