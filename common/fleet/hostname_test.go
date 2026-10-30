package fleet

import (
	"strings"
	"testing"
)

func TestParseHostnameValid(t *testing.T) {
	cases := map[string]Name{
		"12345-TN1-PRI":    {Store: "12345", TN: 1, Role: RolePrimary},
		"12345-TN2-SEC":    {Store: "12345", TN: 2, Role: RoleSecondary},
		"12345-TN3":        {Store: "12345", TN: 3, Role: RoleNone},
		"12345-TN12":       {Store: "12345", TN: 12, Role: RoleNone},
		"12345-tn1-pri":    {Store: "12345", TN: 1, Role: RolePrimary}, // Linux hay để chữ thường
		"  00123-TN4 \n":   {Store: "00123", TN: 4, Role: RoleNone},    // giữ số 0 đầu của mã
		"1234567890-TN999": {Store: "1234567890", TN: 999, Role: RoleNone},
	}
	for in, want := range cases {
		got, err := ParseHostname(in)
		if err != nil || got != want {
			t.Errorf("ParseHostname(%q) = %+v, %v; muốn %+v", in, got, err, want)
		}
	}
	if s := (Name{Store: "12345", TN: 1, Role: RolePrimary}).String(); s != "12345-TN1-PRI" {
		t.Errorf("String() = %q", s)
	}
}

func TestParseHostnameInvalid(t *testing.T) {
	cases := map[string]string{ // hostname → đoạn phải có trong thông báo lỗi
		"12345-TN1":       "phải có hậu tố -PRI (đúng: 12345-TN1-PRI)",
		"12345-TN2":       "phải có hậu tố -SEC (đúng: 12345-TN2-SEC)",
		"12345-TN1-SEC":   "phải có hậu tố -PRI",
		"12345-TN2-PRI":   "phải có hậu tố -SEC",
		"12345-TN3-PRI":   "không có hậu tố -PRI/-SEC (đúng: 12345-TN3)",
		"12345-TN0":       "không đúng mẫu",
		"12345-TN01-PRI":  "không đúng mẫu", // không nhận số 0 đầu ở vị trí
		"12345-TN1000":    "không đúng mẫu",
		"ST12345-TN1":     "không đúng mẫu",
		"12345_TN1_PRI":   "không đúng mẫu",
		"12345-TN1-PRI.":  "không đúng mẫu",
		"ubuntu":          "không đúng mẫu",
		"":                "không đúng mẫu",
		"12345678901-TN3": "không đúng mẫu", // mã quá 10 chữ số
		"../12345-TN3":    "không đúng mẫu",
	}
	for in, want := range cases {
		_, err := ParseHostname(in)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseHostname(%q) lỗi = %v; muốn chứa %q", in, err, want)
		}
	}
}
