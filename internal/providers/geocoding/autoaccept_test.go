package geocoding

import "testing"

func TestAutoAccept(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		results []Result
		want    bool
		wantSF  bool // when want is true, sanity-check we got the right row
	}{
		{
			name:    "empty results",
			query:   "anything",
			results: nil,
		},
		{
			name:  "single unambiguous hit",
			query: "San Francisco",
			results: []Result{
				{Name: "San Francisco", Admin1: "California", Country: "United States"},
			},
			want:   true,
			wantSF: true,
		},
		{
			name:  "casing-insensitive query match",
			query: "san francisco",
			results: []Result{
				{Name: "San Francisco", Admin1: "California", Country: "United States"},
			},
			want:   true,
			wantSF: true,
		},
		{
			name:  "trims whitespace before comparing",
			query: "  San Francisco  ",
			results: []Result{
				{Name: "San Francisco", Admin1: "California", Country: "United States"},
			},
			want:   true,
			wantSF: true,
		},
		{
			name:  "multiple same-name results force picker",
			query: "Springfield",
			results: []Result{
				{Name: "Springfield", Admin1: "Missouri"},
				{Name: "Springfield", Admin1: "Illinois"},
				{Name: "Springfield", Admin1: "Massachusetts"},
			},
		},
		{
			name:  "top hit is unique but other rows are unrelated cities",
			query: "San Francisco",
			results: []Result{
				{Name: "San Francisco", Admin1: "California"},
				{Name: "South San Francisco", Admin1: "California"},
				{Name: "San Francisco de Macorís", Country: "Dominican Republic"},
			},
			want:   true,
			wantSF: true,
		},
		{
			name:  "query does not match top hit Name",
			query: "SF",
			results: []Result{
				{Name: "San Francisco", Admin1: "California"},
			},
		},
		{
			name:  "empty query",
			query: "   ",
			results: []Result{
				{Name: "Anywhere"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := AutoAccept(tc.query, tc.results)
			if ok != tc.want {
				t.Fatalf("AutoAccept(%q) ok = %v, want %v", tc.query, ok, tc.want)
			}
			if tc.want && tc.wantSF {
				if got == nil || got.Name != "San Francisco" {
					t.Fatalf("AutoAccept returned %+v, want San Francisco", got)
				}
			}
			if !tc.want && got != nil {
				t.Fatalf("AutoAccept returned non-nil result %+v when want=false", got)
			}
		})
	}
}
