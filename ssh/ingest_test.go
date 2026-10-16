package ssh

import (
	"errors"
	"strings"
	"testing"

	"github.com/kierank/herald/config"
)

func TestFormatUploadError_ListsEveryFeed(t *testing.T) {
	err := config.FeedErrors{
		{URL: "https://a.example/feed.xml", Err: errors.New("server returned 404 Not Found")},
		{URL: "https://b.example/feed.xml", Err: errors.New("not an RSS, Atom or JSON feed (server sent text/html)")},
	}
	got := formatUploadError("daily.txt", err)

	if !strings.HasPrefix(got, "\n") {
		t.Errorf("output should start on a fresh line after the progress meter, got %q", got)
	}
	for _, want := range []string{
		"rejected daily.txt, 2 feeds could not be used",
		"  https://a.example/feed.xml\n    server returned 404 Not Found\n",
		"  https://b.example/feed.xml\n    not an RSS, Atom or JSON feed (server sent text/html)\n",
		"Nothing was saved.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatUploadError_SingularAndGeneric(t *testing.T) {
	one := formatUploadError("x.txt", config.FeedErrors{{URL: "u", Err: errors.New("e")}})
	if !strings.Contains(one, "1 feed could not be used") {
		t.Errorf("want singular noun, got:\n%s", one)
	}

	generic := formatUploadError("x.txt", errors.New("invalid config: email is required"))
	if !strings.Contains(generic, "rejected x.txt: invalid config: email is required\n") {
		t.Errorf("unexpected generic output:\n%s", generic)
	}
}
