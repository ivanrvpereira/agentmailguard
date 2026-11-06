package fake_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/fake"
	_ "github.com/mattn/go-sqlite3"
)

var now = time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

// seed applies schema.sql to <home>/fake-provider.db by hand, as docs/fake-provider.md shows, and runs the statements.
func seed(t *testing.T, home string, statements ...string) {
	t.Helper()
	db := open(t, home)
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range append([]string{string(schema)}, statements...) {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}
func open(t *testing.T, home string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(home, "fake-provider.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// mailbox seeds the demo account with three inbox messages, an inbox draft, an archived message, and
// a message of another account.
func mailbox(t *testing.T) (model.Mailbox, string) {
	t.Helper()
	home := t.TempDir()
	seed(t, home, `INSERT INTO messages (account, id, folder, sender_name, sender_address, to_addresses, subject, body, content_type, received_at, is_read, categories, attachments, spf, dmarc, compauth, compauth_reason) VALUES
		('demo@fake.test', 'old', 'inbox', 'Ana', 'ana@example.test', '["demo@fake.test"]', 'Old report', 'old body', 'text', '2026-03-01T09:00:00Z', 1, '["Work"]', '["report.pdf"]', 'pass', 'fail', 'fail', '601'),
		('demo@fake.test', 'new', 'inbox', 'Bob', 'bob@example.test', '["demo@fake.test"]', 'Lunch plans', '<p>Pizza?</p>', 'html', '2026-03-31T10:00:00+02:00', 0, '[]', '[]', NULL, NULL, NULL, NULL),
		('demo@fake.test', 'mid', 'inbox', 'Ana', 'ana@example.test', '[]', 'Invoice', 'Pay by Friday', 'text', '2026-03-15T09:00:00Z', 0, '[]', '[]', NULL, NULL, NULL, NULL),
		('demo@fake.test', 'arc', 'archive', 'Carla', 'carla@example.test', '[]', 'Archived invoice', 'old', 'text', '2026-03-20T09:00:00Z', 1, '[]', '[]', NULL, NULL, NULL, NULL),
		('other', 'theirs', 'inbox', 'Ana', 'ana@example.test', '[]', 'Invoice', 'Pay', 'text', '2026-03-31T09:00:00Z', 0, '[]', '[]', NULL, NULL, NULL, NULL)`,
		`INSERT INTO messages (account, id, folder, sender_address, subject, received_at, is_draft) VALUES ('demo@fake.test', 'draft', 'inbox', 'demo@fake.test', 'Draft invoice', '2026-03-31T11:00:00Z', 1)`)
	return fake.New("demo@fake.test", home, func() time.Time { return now }).Mailbox(), home
}

// messages drops the next-page marker of a page.
func messages(p model.MessagePage, err error) ([]model.Message, error) { return p.Messages, err }

func ids(messages []model.Message) []string {
	out := []string{}
	for _, m := range messages {
		out = append(out, m.ID)
	}
	return out
}

func TestListReturnsFolderMessagesNewestFirstWithoutBodies(t *testing.T) {
	m, _ := mailbox(t)
	ctx := context.Background()
	page, err := m.List(ctx, "inbox", model.PageRequest{Size: 2, Page: "1"}, false)
	got := page.Messages
	if err != nil || !reflect.DeepEqual(ids(got), []string{"new", "mid"}) || page.Next != "3" {
		t.Fatalf("ids=%v next=%q err=%v", ids(got), page.Next, err)
	}
	if got[0].Body != nil || got[0].ContentType != nil || got[0].Auth != nil {
		t.Fatal("metadata list carried body or auth")
	}
	want := model.Message{ID: "new", ThreadID: "new", Sender: model.Address{Name: "Bob", Address: "bob@example.test"}, Subject: "Lunch plans", ReceivedAt: time.Date(2026, 3, 31, 8, 0, 0, 0, time.UTC), To: []model.Address{{Address: "demo@fake.test"}}, CC: []model.Address{}, BCC: []model.Address{}, Folders: []string{"inbox"}, Attachments: []string{}, Flag: model.NoFlag, Categories: []string{}}
	got[0].ReceivedAt = got[0].ReceivedAt.UTC()
	if !reflect.DeepEqual(got[0], want) {
		t.Fatalf("message=%+v", got[0])
	}
	for _, call := range []func() error{
		func() error { _, err := m.List(ctx, "nowhere", model.PageRequest{}, false); return err },
	} {
		if err := call(); err == nil {
			t.Error("invalid list accepted")
		}
	}
}

func TestGetReturnsBodyAndAuthOnlyWhenAskedAndFailsOnAnyMissingID(t *testing.T) {
	m, _ := mailbox(t)
	ctx := context.Background()
	got, err := m.Get(ctx, []string{"old", "new"}, true)
	if err != nil || !reflect.DeepEqual(ids(got), []string{"old", "new"}) {
		t.Fatalf("ids=%v err=%v", ids(got), err)
	}
	pass, fail, reason := "pass", "fail", "601"
	old := got[0]
	if old.Body == nil || *old.Body != "old body" || old.ContentType == nil || *old.ContentType != model.Text || !old.IsRead || !reflect.DeepEqual(old.Categories, []string{"Work"}) || !reflect.DeepEqual(old.Attachments, []string{"report.pdf"}) {
		t.Fatalf("message=%+v", old)
	}
	if !reflect.DeepEqual(*old.Auth, model.AuthResults{SPF: &pass, DMARC: &fail, CompAuth: &fail, CompAuthReason: &reason}) {
		t.Fatalf("auth=%+v", *old.Auth)
	}
	if *got[1].ContentType != model.HTML || got[1].Auth == nil || *got[1].Auth != (model.AuthResults{}) {
		t.Fatalf("message=%+v", got[1])
	}
	if got, err := m.Get(ctx, []string{"old"}, false); err != nil || got[0].Body != nil || got[0].Auth != nil {
		t.Fatalf("metadata get=%+v err=%v", got, err)
	}
	for _, missing := range [][]string{{"old", "gone"}, {"theirs"}} {
		if got, err := m.Get(ctx, missing, true); err == nil || len(got) != 0 {
			t.Fatalf("missing %v: got=%v err=%v", missing, ids(got), err)
		}
	}
}

func TestSearchMatchesCaseInsensitiveSubstringsAcrossTheAccount(t *testing.T) {
	m, _ := mailbox(t)
	ctx := context.Background()
	text := func(s string) *string { return &s }
	at := func(s string) *time.Time { d, _ := time.Parse(time.RFC3339, s); return &d }
	archive := "archive"
	for _, tc := range []struct {
		name  string
		query model.SearchQuery
		count int
		want  []string
	}{
		{"text over subject", model.SearchQuery{Text: text("INVOICE")}, 25, []string{"draft", "arc", "mid"}},
		{"text over body", model.SearchQuery{Text: text("pizza")}, 25, []string{"new"}},
		{"text over sender", model.SearchQuery{Text: text("carla@")}, 25, []string{"arc"}},
		{"sender name", model.SearchQuery{Sender: text("ana")}, 25, []string{"mid", "old"}},
		{"subject only", model.SearchQuery{Subject: text("friday")}, 25, []string{}},
		{"after is inclusive and before exclusive", model.SearchQuery{After: at("2026-03-15T09:00:00Z"), Before: at("2026-03-20T09:00:00Z")}, 25, []string{"mid"}},
		{"bounds are exact instants in any zone", model.SearchQuery{After: at("2026-03-15T10:00:01+01:00"), Before: at("2026-03-20T10:00:01+01:00")}, 25, []string{"arc"}},
		{"folder", model.SearchQuery{Text: text("invoice"), Folder: &archive}, 25, []string{"arc"}},
		{"size", model.SearchQuery{Text: text("invoice")}, 2, []string{"draft", "arc"}},
	} {
		got, err := messages(m.Search(ctx, tc.query, model.PageRequest{Size: tc.count}, false))
		if err != nil || !reflect.DeepEqual(ids(got), tc.want) {
			t.Errorf("%s: ids=%v err=%v", tc.name, ids(got), err)
		}
	}
	nowhere := "nowhere"
	if _, err := messages(m.Search(ctx, model.SearchQuery{Folder: &nowhere}, model.PageRequest{}, false)); err == nil {
		t.Error("unknown folder accepted")
	}
}

func TestSearchByRecipientMatchesToAndCcButNotTheSender(t *testing.T) {
	home := t.TempDir()
	seed(t, home, `INSERT INTO messages (account, id, folder, sender_address, to_addresses, cc_addresses, subject, received_at) VALUES
		('demo@fake.test', 'to', 'inbox', 'bob@example.test', '["ana@example.test"]', '[]', 'To Ana', '2026-03-01T09:00:00Z'),
		('demo@fake.test', 'cc', 'inbox', 'bob@example.test', '["carla@example.test"]', '["ana@example.test"]', 'Cc Ana', '2026-03-02T09:00:00Z'),
		('demo@fake.test', 'sender', 'inbox', 'ana@example.test', '["bob@example.test"]', '[]', 'From Ana', '2026-03-03T09:00:00Z'),
		('demo@fake.test', 'nobody', 'inbox', 'ana@example.test', '[]', '[]', 'Note to self', '2026-03-04T09:00:00Z')`)
	m := fake.New("demo@fake.test", home, func() time.Time { return now }).Mailbox()
	to := "ANA@example"
	got, err := messages(m.Search(t.Context(), model.SearchQuery{To: &to}, model.PageRequest{}, false))
	if err != nil || !reflect.DeepEqual(ids(got), []string{"cc", "to"}) {
		t.Fatalf("ids=%v err=%v", ids(got), err)
	}
	// An empty value is no filter, as for the other fields and the real providers.
	empty, subject := "", "self"
	got, err = messages(m.Search(t.Context(), model.SearchQuery{To: &empty, Subject: &subject}, model.PageRequest{}, false))
	if err != nil || !reflect.DeepEqual(ids(got), []string{"nobody"}) {
		t.Fatalf("empty to: ids=%v err=%v", ids(got), err)
	}
}

func TestScanInboxListsNonDraftInboxMailInTheRange(t *testing.T) {
	m, _ := mailbox(t)
	ctx := context.Background()
	since, until := time.Date(2026, 3, 15, 9, 0, 0, 0, time.UTC), time.Date(2026, 3, 31, 8, 0, 0, 0, time.UTC)
	got, err := m.ScanInbox(ctx, since, &until)
	want := model.InboxScan{Messages: []model.InboxEntry{{ID: "mid", ReceivedAt: since}}, Failures: []model.ScanFailure{}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("scan=%+v err=%v", got, err)
	}
	got, err = m.ScanInbox(ctx, since, nil)
	if err != nil || len(got.Messages) != 2 || got.Messages[1].ID != "new" || !got.Messages[1].ReceivedAt.Equal(until) {
		t.Fatalf("open scan=%+v err=%v", got, err)
	}
	if _, err := m.ScanInbox(ctx, since, &since); err == nil {
		t.Fatal("empty range accepted")
	}
}

func TestFoldersMarkTrashSpamAndEveryFolderBelowThem(t *testing.T) {
	m, home := mailbox(t)
	if _, err := m.Folders(context.Background()); err != nil {
		t.Fatal(err)
	}
	seed(t, home, `INSERT INTO folders (account, id, name, parent) VALUES ('demo@fake.test', 'projects', 'Projects', NULL), ('demo@fake.test', 'old', 'Old', 'trash'), ('demo@fake.test', 'older', 'Older', 'old'), ('demo@fake.test', 'junk', 'Junk', 'spam'), ('other', 'theirs', 'Theirs', NULL)`)
	got, err := m.Folders(context.Background())
	want := []model.Folder{{ID: "inbox", Name: "Inbox"}, {ID: "drafts", Name: "Drafts"}, {ID: "sent", Name: "Sent"}, {ID: "archive", Name: "Archive"}, {ID: "trash", Name: "Trash", Bin: model.Trash}, {ID: "spam", Name: "Spam", Bin: model.Spam}, {ID: "projects", Name: "Projects"}, {ID: "old", Name: "Old", Bin: model.Trash}, {ID: "older", Name: "Older", Bin: model.Trash}, {ID: "junk", Name: "Junk", Bin: model.Spam}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("folders=%+v err=%v", got, err)
	}
}

func folderOf(t *testing.T, m model.Mailbox, id string) model.Message {
	t.Helper()
	got, err := m.Get(context.Background(), []string{id}, false)
	if err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	return got[0]
}

func TestMutationsChangeMessagesAndReportMissingIDsInOrder(t *testing.T) {
	m, home := mailbox(t)
	ctx := context.Background()
	seed(t, home, `INSERT INTO categories (account, name) VALUES ('demo@fake.test', 'Work'), ('demo@fake.test', 'Home'), ('other', 'Theirs')`)
	if got, err := m.Categories(ctx); err != nil || !reflect.DeepEqual(got, []string{"Work", "Home"}) {
		t.Fatalf("categories=%v err=%v", got, err)
	}
	results, err := m.Trash(ctx, []string{"old", "gone", "theirs", "mid"})
	if err != nil || len(results) != 4 {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	for i, id := range []string{"old", "gone", "theirs", "mid"} {
		r := results[i]
		ok := id == "old" || id == "mid"
		if r.ID != id || r.OK != ok || (r.Error == nil) != ok || (!ok && model.ValidateFailureText(r.Error) != nil) {
			t.Errorf("result %d=%+v", i, r)
		}
	}
	// Trash is a move: the message still exists, in the trash folder.
	if got := folderOf(t, m, "old"); !reflect.DeepEqual(got.Folders, []string{"trash"}) {
		t.Fatalf("trashed folders=%v", got.Folders)
	}
	if got, err := messages(m.List(ctx, "trash", model.PageRequest{}, false)); err != nil || !reflect.DeepEqual(ids(got), []string{"mid", "old"}) {
		t.Fatalf("trash=%v err=%v", ids(got), err)
	}
	seed(t, home, `INSERT INTO folders (account, id, name) VALUES ('demo@fake.test', 'projects', 'Projects')`)
	for _, call := range []func() ([]model.MutationResult, error){
		func() ([]model.MutationResult, error) { return m.Archive(ctx, []string{"new"}) },
		func() ([]model.MutationResult, error) { return m.Move(ctx, []string{"arc"}, "archive", "projects") },
		func() ([]model.MutationResult, error) { return m.MarkRead(ctx, []string{"new"}, true) },
		func() ([]model.MutationResult, error) { return m.Flag(ctx, []string{"new"}, model.Complete) },
		func() ([]model.MutationResult, error) {
			return m.Categorize(ctx, []string{"old"}, []string{"Home"}, []string{"Work"})
		},
	} {
		if r, err := call(); err != nil || len(r) != 1 || !r[0].OK {
			t.Fatalf("result=%+v err=%v", r, err)
		}
	}
	if got := folderOf(t, m, "new"); !reflect.DeepEqual(got.Folders, []string{"archive"}) || !got.IsRead || got.Flag != model.Complete {
		t.Fatalf("new=%+v", got)
	}
	if got := folderOf(t, m, "arc"); !reflect.DeepEqual(got.Folders, []string{"projects"}) {
		t.Fatalf("moved folders=%v", got.Folders)
	}
	if got := folderOf(t, m, "old"); !reflect.DeepEqual(got.Categories, []string{"Home"}) {
		t.Fatalf("categories=%v", got.Categories)
	}
	if _, err := m.Move(ctx, []string{"new"}, "archive", "nowhere"); err == nil {
		t.Fatal("move to an unknown folder accepted")
	}
}

func TestStatusGivesTheAccountAddressWithoutScopes(t *testing.T) {
	home := t.TempDir()
	p := fake.New("demo@fake.test", home, nil)
	if address, scopes, err := p.Status(context.Background()); err != nil || address != "demo@fake.test" || scopes == nil || len(scopes) != 0 {
		t.Fatalf("address=%q scopes=%v err=%v", address, scopes, err)
	}
}

func TestDraftsLandInDraftsFromTheAccountAddress(t *testing.T) {
	m, _ := mailbox(t)
	ctx := context.Background()
	draft, err := m.DraftNew(ctx, model.NewDraft{To: []string{"ana@example.test"}, Subject: "Hello", Body: "first"})
	if err != nil {
		t.Fatal(err)
	}
	created := draft.ID
	replied, err := m.DraftReply(ctx, "old", model.Reply{Body: "Thanks"})
	reply := replied.ID
	if err != nil {
		t.Fatal(err)
	}
	forwarded, err := m.DraftForward(ctx, "new", model.Forward{Body: "FYI", To: []string{"x@example.test"}})
	forward := forwarded.ID
	if err != nil {
		t.Fatal(err)
	}
	if updated, thread, err := m.DraftUpdate(ctx, created, model.DraftChange{Body: new("second")}); err != nil || updated != created || thread != created {
		t.Fatalf("update=%q thread=%q err=%v", updated, thread, err)
	}
	got, err := m.Get(ctx, []string{created, reply, forward}, true)
	if err != nil {
		t.Fatal(err)
	}
	type view struct {
		To, Folders   []string
		Subject, Body string
		Type          model.ContentType
	}
	want := []view{
		{[]string{"ana@example.test"}, []string{"drafts"}, "Hello", "second", model.Text},
		{[]string{"ana@example.test"}, []string{"drafts"}, "Re: Old report", "Thanks", model.Text},
		{[]string{"x@example.test"}, []string{"drafts"}, "Fw: Lunch plans", "FYI", model.Text},
	}
	for i, d := range got {
		to := []string{}
		for _, a := range d.To {
			to = append(to, a.Address)
		}
		if v := (view{to, d.Folders, d.Subject, *d.Body, *d.ContentType}); !reflect.DeepEqual(v, want[i]) || !d.IsDraft || d.Sender != (model.Address{Address: "demo@fake.test"}) || !d.ReceivedAt.Equal(now) {
			t.Errorf("draft %d=%+v", i, d)
		}
	}
	// Only a draft changes; a received message is not a draft.
	for _, id := range []string{"old", "gone"} {
		if _, _, err := m.DraftUpdate(ctx, id, model.DraftChange{Body: new("changed")}); err == nil {
			t.Errorf("update of %s accepted", id)
		}
	}
	for _, call := range []func() error{
		func() error { _, err := m.DraftReply(ctx, "gone", model.Reply{Body: "x"}); return err },
		func() error {
			_, err := m.DraftForward(ctx, "theirs", model.Forward{To: []string{"x@example.test"}})
			return err
		},
	} {
		if call() == nil {
			t.Error("draft from a missing message accepted")
		}
	}
}

// Draft delete and draft send act only on a draft: a received message is refused and stays as it is, and a missing
// message is refused.
func TestDraftDeleteAndSendRefuseAMessageThatIsNotADraft(t *testing.T) {
	m, _ := mailbox(t)
	ctx := context.Background()
	before, err := m.Get(ctx, []string{"old"}, true)
	if err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func(string) error{
		"delete": func(id string) error { return m.DraftDelete(ctx, id) },
		"send":   func(id string) error { return m.DraftSend(ctx, id) },
	} {
		for _, id := range []string{"old", "gone"} {
			if err := call(id); err == nil {
				t.Errorf("%s of %s accepted", name, id)
			}
		}
	}
	if after, err := m.Get(ctx, []string{"old"}, true); err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("received message changed: %+v %v", after, err)
	}
}

func TestSendsLandInSentAndRecordKindAndSource(t *testing.T) {
	m, home := mailbox(t)
	ctx := context.Background()
	for _, call := range []func() error{
		func() error {
			return m.SendNew(ctx, model.NewDraft{To: []string{"ana@example.test"}, Subject: "Hello", Body: "hi"})
		},
		func() error { return m.SendReply(ctx, "old", model.Reply{Body: "Thanks"}) },
		func() error { return m.SendForward(ctx, "new", model.Forward{To: []string{"x@example.test"}}) },
	} {
		if err := call(); err != nil {
			t.Fatal(err)
		}
	}
	if m.SendReply(ctx, "gone", model.Reply{Body: "x"}) == nil {
		t.Fatal("reply to a missing message accepted")
	}
	sent, err := messages(m.List(ctx, "sent", model.PageRequest{}, false))
	if err != nil || len(sent) != 3 {
		t.Fatalf("sent=%v err=%v", ids(sent), err)
	}
	seeded := map[string]bool{"old": true, "new": true, "mid": true, "arc": true, "draft": true, "theirs": true}
	for _, s := range sent {
		if s.IsDraft || !s.IsRead || seeded[s.ID] || s.Sender.Address != "demo@fake.test" {
			t.Errorf("sent=%+v", s)
		}
		seeded[s.ID] = true
	}
	rows, err := open(t, home).Query(`SELECT subject, origin, coalesce(source_id, '') FROM messages WHERE account = 'demo@fake.test' AND folder = 'sent' ORDER BY subject`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := [][3]string{}
	for rows.Next() {
		var r [3]string
		if err := rows.Scan(&r[0], &r[1], &r[2]); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if want := [][3]string{{"Fw: Lunch plans", "forward", "new"}, {"Hello", "new", ""}, {"Re: Old report", "reply", "old"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sent rows=%v", got)
	}
	if drafts, err := messages(m.List(ctx, "drafts", model.PageRequest{}, false)); err != nil || len(drafts) != 0 {
		t.Fatalf("drafts=%v err=%v", ids(drafts), err)
	}
}

func TestFakeDBIsPrivate(t *testing.T) {
	fresh, seeded := t.TempDir(), t.TempDir()
	// A file made by hand with sqlite3 gets the default mode.
	seed(t, seeded)
	if err := os.Chmod(filepath.Join(seeded, "fake-provider.db"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, home := range []string{fresh, seeded} {
		if _, _, err := fake.New("demo@fake.test", home, nil).Status(context.Background()); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(filepath.Join(home, "fake-provider.db")); err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("mode=%v err=%v", info.Mode(), err)
		}
	}
}

// A sqlite3 session that holds the write lock delays a call; the call does not fail.
func TestWritesWaitForAnotherWriter(t *testing.T) {
	m, home := mailbox(t)
	ctx := context.Background()
	if _, err := m.Folders(ctx); err != nil {
		t.Fatal(err)
	}
	other, err := open(t, home).Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- m.SendNew(ctx, model.NewDraft{To: []string{"ana@example.test"}, Subject: "Hello", Body: "hi"})
	}()
	time.Sleep(200 * time.Millisecond)
	if _, err := other.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if sent, err := messages(m.List(ctx, "sent", model.PageRequest{}, false)); err != nil || len(sent) != 1 {
		t.Fatalf("sent=%v err=%v", ids(sent), err)
	}
}

func TestSeededMailWithoutATimeIsReceivedNow(t *testing.T) {
	m, home := mailbox(t)
	seed(t, home, `INSERT INTO messages (account, id, subject) VALUES ('demo@fake.test', 'quick', 'No time')`)
	got := folderOf(t, m, "quick")
	if d := time.Since(got.ReceivedAt); d < -time.Minute || d > time.Minute || !reflect.DeepEqual(got.Folders, []string{"inbox"}) {
		t.Fatalf("message=%+v", got)
	}
}

// threads maps each message ID to its thread ID.
func threads(messages []model.Message) map[string]string {
	out := map[string]string{}
	for _, m := range messages {
		out[m.ID] = m.ThreadID
	}
	return out
}

func TestMessagesCarryTheirSeededThreadOrTheirOwnID(t *testing.T) {
	m, home := mailbox(t)
	ctx := t.Context()
	seed(t, home, `UPDATE messages SET thread_id = 'invoices' WHERE id IN ('old', 'mid')`)
	invoice := "invoice"
	list, err := messages(m.List(ctx, "inbox", model.PageRequest{}, false))
	if err != nil {
		t.Fatal(err)
	}
	search, err := messages(m.Search(ctx, model.SearchQuery{Text: &invoice}, model.PageRequest{}, false))
	if err != nil {
		t.Fatal(err)
	}
	get, err := m.Get(ctx, []string{"mid", "arc"}, true)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ got, want map[string]string }{
		"list":   {threads(list), map[string]string{"old": "invoices", "mid": "invoices", "new": "new", "draft": "draft"}},
		"search": {threads(search), map[string]string{"mid": "invoices", "arc": "arc", "draft": "draft"}},
		"get":    {threads(get), map[string]string{"mid": "invoices", "arc": "arc"}},
	} {
		if !reflect.DeepEqual(tc.got, tc.want) {
			t.Errorf("%s threads=%v want %v", name, tc.got, tc.want)
		}
	}
}

func TestRepliesAndForwardsJoinTheSourceConversationAndNewMailStartsItsOwn(t *testing.T) {
	m, home := mailbox(t)
	ctx := t.Context()
	seed(t, home, `UPDATE messages SET thread_id = 'invoices' WHERE id = 'old'`)
	draft, err := m.DraftNew(ctx, model.NewDraft{To: []string{"ana@example.test"}, Subject: "Hello", Body: "first"})
	if err != nil {
		t.Fatal(err)
	}
	created := draft.ID
	replied, err := m.DraftReply(ctx, "old", model.Reply{Body: "Thanks"})
	reply := replied.ID
	if err != nil {
		t.Fatal(err)
	}
	forwarded, err := m.DraftForward(ctx, "new", model.Forward{To: []string{"x@example.test"}})
	forward := forwarded.ID
	if err != nil {
		t.Fatal(err)
	}
	drafts, err := m.Get(ctx, []string{created, reply, forward}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := threads(drafts), map[string]string{created: created, reply: "invoices", forward: "new"}; !reflect.DeepEqual(got, want) {
		t.Errorf("draft threads=%v want %v", got, want)
	}
	for _, send := range []func() error{
		func() error {
			return m.SendNew(ctx, model.NewDraft{To: []string{"ana@example.test"}, Subject: "Hello", Body: "hi"})
		},
		func() error { return m.SendReply(ctx, "old", model.Reply{Body: "Thanks"}) },
		func() error { return m.SendForward(ctx, "new", model.Forward{To: []string{"x@example.test"}}) },
	} {
		if err := send(); err != nil {
			t.Fatal(err)
		}
	}
	sent, err := messages(m.List(ctx, "sent", model.PageRequest{}, false))
	if err != nil {
		t.Fatal(err)
	}
	bySubject := map[string]string{}
	for _, s := range sent {
		bySubject[s.Subject] = s.ThreadID
		if s.Subject == "Hello" && s.ThreadID != s.ID {
			t.Errorf("new send thread=%q id=%q", s.ThreadID, s.ID)
		}
	}
	if want := map[string]string{"Hello": bySubject["Hello"], "Re: Old report": "invoices", "Fw: Lunch plans": "new"}; !reflect.DeepEqual(bySubject, want) {
		t.Errorf("sent threads=%v want %v", bySubject, want)
	}
}

// A fake-provider.db made before messages had thread_id gets the column on the first open.
func TestDatabaseFromBeforeThreadsOpensAndKeepsItsMail(t *testing.T) {
	home := t.TempDir()
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	before := strings.Replace(string(schema), "\tthread_id TEXT,\n", "", 1)
	if before == string(schema) {
		t.Fatal("schema.sql has no thread_id column")
	}
	db := open(t, home)
	for _, s := range []string{before, `INSERT INTO messages (account, id, subject, received_at) VALUES ('demo@fake.test', 'old', 'Old report', '2026-03-01T09:00:00Z')`} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	m := fake.New("demo@fake.test", home, func() time.Time { return now }).Mailbox()
	ctx := t.Context()
	list, err := messages(m.List(ctx, "inbox", model.PageRequest{}, false))
	if err != nil || !reflect.DeepEqual(threads(list), map[string]string{"old": "old"}) {
		t.Fatalf("list threads=%v err=%v", threads(list), err)
	}
	replied, err := m.DraftReply(ctx, "old", model.Reply{Body: "Thanks"})
	reply := replied.ID
	if err != nil {
		t.Fatal(err)
	}
	if got, err := m.Get(ctx, []string{reply}, false); err != nil || got[0].ThreadID != "old" {
		t.Fatalf("reply=%+v err=%v", got, err)
	}
	reopened := fake.New("demo@fake.test", home, func() time.Time { return now }).Mailbox()
	if got, err := reopened.Get(ctx, []string{"old", reply}, false); err != nil || !reflect.DeepEqual(threads(got), map[string]string{"old": "old", reply: "old"}) {
		t.Fatalf("reopened threads=%v err=%v", threads(got), err)
	}
}

// folders maps each message ID to its folders.
func folders(messages []model.Message) map[string][]string {
	out := map[string][]string{}
	for _, m := range messages {
		out[m.ID] = m.Folders
	}
	return out
}

func TestThreadReturnsEveryMessageOfTheConversationNewestFirstFromEveryFolder(t *testing.T) {
	m, home := mailbox(t)
	ctx := t.Context()
	seed(t, home, `INSERT INTO messages (account, id, folder, sender_address, subject, received_at, is_draft, thread_id) VALUES
		('demo@fake.test', 'sent-reply', 'sent', 'demo@fake.test', 'Re: Old report', '2026-03-02T09:00:00Z', 0, 'old'),
		('demo@fake.test', 'binned', 'trash', 'ana@example.test', 'Re: Old report', '2026-03-03T09:00:00+01:00', 0, 'old'),
		('demo@fake.test', 'reply-draft', 'drafts', 'demo@fake.test', 'Re: Old report', '2026-03-04T09:00:00Z', 1, 'old'),
		('other', 'their-reply', 'inbox', 'ana@example.test', 'Re: Old report', '2026-03-05T09:00:00Z', 0, 'old')`)
	got, err := m.Thread(ctx, "old")
	if err != nil || !reflect.DeepEqual(ids(got), []string{"reply-draft", "binned", "sent-reply", "old"}) {
		t.Fatalf("ids=%v err=%v", ids(got), err)
	}
	if want := map[string][]string{"old": {"inbox"}, "sent-reply": {"sent"}, "binned": {"trash"}, "reply-draft": {"drafts"}}; !reflect.DeepEqual(folders(got), want) {
		t.Errorf("folders=%v", folders(got))
	}
	if got[3].Body != nil || got[3].Auth != nil || !got[0].IsDraft || got[0].ThreadID != "old" {
		t.Errorf("messages=%+v", got)
	}
	for _, unknown := range []string{"gone", "theirs"} {
		if got, err := m.Thread(ctx, unknown); !errors.Is(err, model.MailboxError("NotFoundError")) || len(got) != 0 {
			t.Errorf("%s: ids=%v err=%v", unknown, ids(got), err)
		}
	}
}

func TestThreadMessagesInKeepsTheConversationMessagesOfOneFolderNewestFirst(t *testing.T) {
	m, home := mailbox(t)
	ctx := t.Context()
	seed(t, home, `INSERT INTO messages (account, id, folder, sender_address, subject, received_at, is_draft, thread_id) VALUES
		('demo@fake.test', 'inbox-reply', 'inbox', 'ana@example.test', 'Re: Old report', '2026-03-05T09:00:00Z', 0, 'old'),
		('demo@fake.test', 'sent-reply', 'sent', 'demo@fake.test', 'Re: Old report', '2026-03-02T09:00:00Z', 0, 'old'),
		('demo@fake.test', 'reply-draft', 'drafts', 'demo@fake.test', 'Re: Old report', '2026-03-04T09:00:00Z', 1, 'old'),
		('demo@fake.test', 'binned-reply', 'trash', 'ana@example.test', 'Re: Old report', '2026-03-07T09:00:00Z', 0, 'old'),
		('demo@fake.test', 'filed-reply', 'old-bin', 'ana@example.test', 'Re: Old report', '2026-03-08T09:00:00Z', 0, 'old'),
		('other', 'their-reply', 'inbox', 'ana@example.test', 'Re: Old report', '2026-03-06T09:00:00Z', 0, 'old')`)
	seed(t, home, `INSERT INTO folders (account, id, name, parent) VALUES ('demo@fake.test', 'old-bin', 'Old', 'trash')`)
	// The common folder names work in any case; other folder IDs are as given.
	for folder, want := range map[string][]string{"INBOX": {"inbox-reply", "old"}, "sent": {"sent-reply"}, "archive": {}, "Trash": {"binned-reply"}, "old-bin": {"filed-reply"}} {
		if got, err := m.ThreadMessagesIn(ctx, "old", folder); err != nil || got == nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: ids=%#v err=%v", folder, got, err)
		}
		if list, err := m.List(ctx, folder, model.PageRequest{}, false); err != nil || len(list.Messages) < len(want) {
			t.Errorf("%s: list=%v err=%v", folder, ids(list.Messages), err)
		}
		if found, err := m.Search(ctx, model.SearchQuery{Folder: &folder}, model.PageRequest{}, false); err != nil || len(found.Messages) < len(want) {
			t.Errorf("%s: search=%v err=%v", folder, ids(found.Messages), err)
		}
	}
	if got, err := m.ThreadMessagesIn(ctx, "old", "SENT"); !errors.Is(err, model.MailboxError("NotFoundError")) || len(got) != 0 {
		t.Errorf("SENT is no folder ID: ids=%v err=%v", got, err)
	}
	for _, unknown := range []string{"gone", "theirs"} {
		if got, err := m.ThreadMessagesIn(ctx, unknown, "inbox"); !errors.Is(err, model.MailboxError("NotFoundError")) || len(got) != 0 {
			t.Errorf("%s: ids=%v err=%v", unknown, got, err)
		}
	}
	if got, err := m.ThreadMessagesIn(ctx, "old", "nowhere"); !errors.Is(err, model.MailboxError("NotFoundError")) || len(got) != 0 {
		t.Errorf("unknown folder: ids=%v err=%v", got, err)
	}
}

// The fake pages like Graph $top: 10 by default, at most 1,000. Its marker is the decimal offset of the next page;
// any other marker is refused.
func TestListAndSearchReturnOnePageAndTheNextMarker(t *testing.T) {
	home := t.TempDir()
	rows := []string{}
	for i := range 12 {
		rows = append(rows, fmt.Sprintf(`('demo', 'm%02d', 'Invoice', '2026-03-01T%02d:00:00Z')`, i, i))
	}
	seed(t, home, `INSERT INTO messages (account, id, subject, received_at) VALUES `+strings.Join(rows, ","))
	m := fake.New("demo", home, func() time.Time { return now }).Mailbox()
	ctx := t.Context()
	invoice := "invoice"
	for name, call := range map[string]func(model.PageRequest) (model.MessagePage, error){
		"list": func(p model.PageRequest) (model.MessagePage, error) { return m.List(ctx, "inbox", p, false) },
		"search": func(p model.PageRequest) (model.MessagePage, error) {
			return m.Search(ctx, model.SearchQuery{Text: &invoice}, p, false)
		},
	} {
		first, err := call(model.PageRequest{})
		if err != nil || len(first.Messages) != 10 || first.Messages[0].ID != "m11" || first.Next != "10" {
			t.Fatalf("%s first: ids=%v next=%q err=%v", name, ids(first.Messages), first.Next, err)
		}
		last, err := call(model.PageRequest{Page: first.Next})
		if err != nil || !reflect.DeepEqual(ids(last.Messages), []string{"m01", "m00"}) || last.Next != "" {
			t.Fatalf("%s last: ids=%v next=%q err=%v", name, ids(last.Messages), last.Next, err)
		}
		if all, err := call(model.PageRequest{Size: 1000}); err != nil || len(all.Messages) != 12 || all.Next != "" {
			t.Fatalf("%s size 1000: %d next=%q err=%v", name, len(all.Messages), all.Next, err)
		}
		for _, p := range []model.PageRequest{{Size: 1001}, {Size: -1}, {Page: "x"}, {Page: "-1"}, {Page: "01"}, {Page: " 1"}} {
			if got, err := call(p); err == nil || got.Messages != nil {
				t.Errorf("%s %+v accepted: %v", name, p, ids(got.Messages))
			}
		}
	}
}

// A fake-provider.db made before the bcc_addresses column gets it on the first open, and new mail keeps its
// To, CC, and BCC lists and its body type. The draft result is the new message in its own conversation.
func TestOlderDatabaseGetsTheBccColumnAndNewMailKeepsEveryRecipientList(t *testing.T) {
	home := t.TempDir()
	seed(t, home, `ALTER TABLE messages DROP COLUMN bcc_addresses`)
	m := fake.New("demo@fake.test", home, func() time.Time { return now }).Mailbox()
	ctx := t.Context()
	d := model.NewDraft{To: []string{"ana@example.test"}, CC: []string{"cy@example.test"}, BCC: []string{"ed@example.test", "flo@example.test"}, Subject: "Hello", Body: "<p>Hi</p>", HTML: true}
	draft, err := m.DraftNew(ctx, d)
	if want := (model.DraftResult{ID: draft.ID, ThreadID: draft.ID, To: d.To, CC: d.CC, BCC: d.BCC, Subject: "Hello"}); err != nil || draft.ID == "" || !reflect.DeepEqual(draft, want) {
		t.Fatalf("draft=%+v err=%v", draft, err)
	}
	if err := m.SendNew(ctx, model.NewDraft{BCC: []string{"ed@example.test"}, Body: "line one\nline two"}); err != nil {
		t.Fatal(err)
	}
	sent, err := messages(m.List(ctx, "sent", model.PageRequest{}, true))
	if err != nil || len(sent) != 1 {
		t.Fatalf("sent=%v err=%v", sent, err)
	}
	got, err := m.Get(ctx, []string{draft.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	address := func(as ...string) []model.Address {
		out := []model.Address{}
		for _, a := range as {
			out = append(out, model.Address{Address: a})
		}
		return out
	}
	if g := got[0]; !reflect.DeepEqual([][]model.Address{g.To, g.CC, g.BCC}, [][]model.Address{address("ana@example.test"), address("cy@example.test"), address("ed@example.test", "flo@example.test")}) || *g.ContentType != model.HTML || *g.Body != "<p>Hi</p>" {
		t.Fatalf("draft=%+v", g)
	}
	if s := sent[0]; !reflect.DeepEqual([][]model.Address{s.To, s.CC, s.BCC}, [][]model.Address{address(), address(), address("ed@example.test")}) || *s.ContentType != model.Text || *s.Body != "line one\nline two" {
		t.Fatalf("sent=%+v", s)
	}
}

// Reply all follows the Gmail rule: the sender, and the original To and CC as copies, without the account's address
// and without duplicates ignoring case; for a sent message, the original To and CC. The agent's CC and BCC are added.
// An HTML reply keeps the quoted original below the agent's HTML. ReplyRecipients gives the same recipients.
func TestReplyAllFollowsTheGmailRuleAndHTMLRepliesQuoteTheOriginal(t *testing.T) {
	m, home := mailbox(t)
	ctx := t.Context()
	seed(t, home, `UPDATE messages SET to_addresses = '["DEMO@fake.test", "bo@example.test"]', cc_addresses = '["Bo@Example.test", "cy@example.test"]' WHERE id = 'old'`,
		`INSERT INTO messages (account, id, folder, sender_address, to_addresses, cc_addresses, subject, body, received_at) VALUES ('demo@fake.test', 'mine', 'sent', 'demo@fake.test', '["dan@example.test", "demo@fake.test"]', '["eve@example.test"]', 'Plan', 'see <you>', '2026-03-02T09:00:00Z')`)
	if got, err := m.ReplyRecipients(ctx, "old", true); err != nil || !reflect.DeepEqual(got, model.Recipients{To: []string{"ana@example.test"}, CC: []string{"bo@example.test", "cy@example.test"}, BCC: []string{}}) {
		t.Fatalf("recipients=%+v %v", got, err)
	}
	draft, err := m.DraftReply(ctx, "old", model.Reply{Body: "Thanks", All: true, CC: []string{"CY@example.test", "flo@example.test"}, BCC: []string{"gus@example.test"}})
	want := model.DraftResult{ID: draft.ID, ThreadID: "old", To: []string{"ana@example.test"}, CC: []string{"bo@example.test", "cy@example.test", "flo@example.test"}, BCC: []string{"gus@example.test"}, Subject: "Re: Old report"}
	if err != nil || draft.ID == "" || !reflect.DeepEqual(draft, want) {
		t.Fatalf("draft=%+v %v", draft, err)
	}
	sent, err := m.DraftReply(ctx, "mine", model.Reply{Body: "<p>Hi</p>", HTML: true, All: true})
	if err != nil || !reflect.DeepEqual([][]string{sent.To, sent.CC}, [][]string{{"dan@example.test"}, {"eve@example.test"}}) {
		t.Fatalf("sent reply=%+v %v", sent, err)
	}
	got, err := m.Get(ctx, []string{draft.ID, sent.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	if g := got[0]; !reflect.DeepEqual(g.BCC, []model.Address{{Address: "gus@example.test"}}) || *g.Body != "Thanks" || *g.ContentType != model.Text {
		t.Fatalf("draft=%+v", g)
	}
	if g := got[1]; *g.ContentType != model.HTML || *g.Body != "<p>Hi</p><blockquote>see &lt;you&gt;</blockquote>" {
		t.Fatalf("html reply=%q", *g.Body)
	}
}
