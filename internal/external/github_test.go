package external

import "testing"

func TestParseGitInput(t *testing.T) {
	ok := map[string]GitRepo{
		"golang/go":                       {Owner: "golang", Repo: "go"},
		"golang/go@master":                {Owner: "golang", Repo: "go", Branch: "master"},
		"a/b@feature/x":                   {Owner: "a", Repo: "b", Branch: "feature/x"},
		"https://github.com/golang/go":    {Owner: "golang", Repo: "go"},
		"github.com/golang/go.git":        {Owner: "golang", Repo: "go"},
		"https://github.com/a/b/tree/dev": {Owner: "a", Repo: "b", Branch: "dev"},
		"https://github.com/a/b/blob/dev/f/x.go?x=1#f": {Owner: "a", Repo: "b", Branch: "dev"},
	}
	for in, want := range ok {
		got, err := ParseGitInput(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got != want {
			t.Fatalf("%q: dapat %+v, mau %+v", in, got, want)
		}
	}
	bad := []string{"", "golang", "a/b/c", "https://gitlab.com/a/b", "a/b@c/../x", "a b/c"}
	for _, in := range bad {
		if _, err := ParseGitInput(in); err == nil {
			t.Fatalf("%q: harus error", in)
		}
	}
}
