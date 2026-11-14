package hostname

import "testing"

func TestFromURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.mollie.com/v2?key=x":    "api.mollie.com",
		"https://User:pw@API.Chift.eu:443/x": "api.chift.eu",
		"http://app.localhost":               "",
		"http://10.0.0.1/x":                  "",
		"http://minio:9000":                  "",
		"https://app.example.com":            "",
		"https://%s.chift.app":               "",
		"https://a.com,https://b.com":        "",
		"https://${HOST}/x":                  "",
		"widgets-shop.be":                    "",
	} {
		if got := FromURL(in); got != want {
			t.Errorf("FromURL(%q) = %q, want %q", in, got, want)
		}
	}
}
