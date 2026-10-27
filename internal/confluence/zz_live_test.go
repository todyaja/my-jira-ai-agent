//go:build live

package confluence

import (
	"context"
	"os"
	"testing"
)

func TestLiveOpenComments(t *testing.T) {
	client := &Client{BaseURL: os.Getenv("JIRA_BASE_URL"), Email: os.Getenv("JIRA_EMAIL"), Token: os.Getenv("JIRA_API_TOKEN"), SpaceKey: os.Getenv("CONFLUENCE_SPACE_KEY")}
	comments, err := client.OpenComments(context.Background(), os.Getenv("LIVE_PAGE_ID"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d open comments", len(comments))
	for _, c := range comments {
		t.Logf("%+v", c)
	}
}
