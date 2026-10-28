package external

import "testing"

func TestShutterstockExtractID(t *testing.T) {
	ok := map[string]string{
		"2583067397": "2583067397",
		"https://www.shutterstock.com/id/image-photo/kendari-southeast-sulawesi-indonesia-november-20-2583067397?trackingId=20bd20a2-6eaa-4657-bbaf-9a8e58814737&listId=searchResults": "2583067397",
		"https://www.shutterstock.com/image-photo/kendari-southeast-sulawesi-indonesia-november-20-2583067397":                                                                         "2583067397",
		"https://www.shutterstock.com/image-vector/icon-set-1234567890#detail":                                                                                                         "1234567890",
		"https://www.shutterstock.com/shutterstock/photos/2583067397/display_1500/stock-photo-2583067397.jpg":                                                                          "2583067397",
		"https://www.shutterstock.com/id/image-illustration/some-art-9876543210?x=1":                                                                                                   "9876543210",
	}
	for in, want := range ok {
		if got := ShutterstockExtractID(in); got != want {
			t.Fatalf("%q: dapat %q, mau %q", in, got, want)
		}
	}
	bad := []string{"", "  ", "shutterstock", "https://www.shutterstock.com/", "https://example.com/foo-2583067397", "abc"}
	for _, in := range bad {
		if got := ShutterstockExtractID(in); got != "" {
			t.Fatalf("%q: harus kosong, dapat %q", in, got)
		}
	}
}
