package gmail_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
)

func TestClosedFoldersAndResolveFolder(t *testing.T) {
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/labels" || r.URL.RawQuery != "" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		respond(w, map[string]any{"labels": []any{
			map[string]string{"id": "TRASH", "type": "system"},
			map[string]string{"id": "SPAM", "type": "system"},
			map[string]string{"id": "Label_1", "name": "Projects", "type": "user"},
		}})
	}))
	defer api.Close()
	box := gmail.New(account, validStore(), gmail.Options{HTTPClient: api.Client(), GmailURL: api.URL}).Mailbox()
	closed, err := box.ClosedFolders(context.Background())
	if err != nil || !reflect.DeepEqual(closed, map[string]model.Bin{"TRASH": model.Trash, "SPAM": model.Spam}) || calls != 0 {
		t.Fatalf("closed=%v err=%v calls=%d", closed, err, calls)
	}
	for _, tc := range []struct{ folder, want string }{{"trash", "TRASH"}, {"sPaM", "SPAM"}, {"Label_1", "Label_1"}} {
		id, err := box.ResolveFolder(context.Background(), tc.folder)
		if err != nil || id != tc.want {
			t.Fatalf("resolve %s = %q, %v", tc.folder, id, err)
		}
	}
	if id, err := box.ResolveFolder(context.Background(), "missing"); err == nil || id != "" {
		t.Fatalf("unknown folder = %q, %v", id, err)
	}
	if calls != 4 {
		t.Fatalf("label requests=%d", calls)
	}
}
