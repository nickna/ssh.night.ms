package chat

import (
	"reflect"
	"testing"
)

func TestExtractImageURLs(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"empty", "", nil},
		{"plain text", "hello world", nil},
		{"single png", "look https://example.com/cat.png", []string{"https://example.com/cat.png"}},
		{"trailing dot", "look https://example.com/cat.png.", []string{"https://example.com/cat.png"}},
		{"jpg + jpeg both", "a https://x/y.jpg b https://x/z.jpeg", []string{"https://x/y.jpg", "https://x/z.jpeg"}},
		{"http allowed", "see http://insecure.example/photo.gif now", []string{"http://insecure.example/photo.gif"}},
		{"ftp rejected", "got ftp://host/img.png", nil},
		{"text:// rejected", "fake text://something.png", nil},
		{"non-image extension", "click https://example.com/page.html", nil},
		{"dedupe same url twice", "a https://x/y.png and again https://x/y.png", []string{"https://x/y.png"}},
		{"paren wrapper", "(https://example.com/cat.png)", []string{"https://example.com/cat.png"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractImageURLs(tc.body)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v want %#v", got, tc.want)
			}
		})
	}
}
