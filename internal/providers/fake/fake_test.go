package fake_test

import (
	"context"
	"database/sql"
	"errors"
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

// seed applies schema.sql to <home>/fake-provider.db by hand, as the README shows, and runs the statements.
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
		('demo', 'old', 'inbox', 'Ana', 'ana@example.test', '["demo@fake.test"]', 'Old report', 'old body', 'text', '2026-03-01T09:00:00Z', 1, '["Work"]', '["report.pdf"]', 'pass', 'fail', 'fail', '601'),
		('demo', 'new', 'inbox', 'Bob', 'bob@example.test', '["demo@fake.test"]', 'Lunch plans', '<p>Pizza?</p>', 'html', '2026-03-31T10:00:00+02:00', 0, '[]', '[]', NULL, NULL, NULL, NULL),
		('demo', 'mid', 'inbox', 'Ana', 'ana@example.test', '[]', 'Invoice', 'Pay by Friday', 'text', '2026-03-15T09:00:00Z', 0, '[]', '[]', NULL, NULL, NULL, NULL),
		('demo', 'arc', 'archive', 'Carla', 'carla@example.test', '[]', 'Archived invoice', 'old', 'text', '2026-03-20T09:00:00Z', 1, '[]', '[]', NULL, NULL, NULL, NULL),
		('other', 'theirs', 'inbox', 'Ana', 'ana@example.test', '[]', 'Invoice', 'Pay', 'text', '2026-03-31T09:00:00Z', 0, '[]', '[]', NULL, NULL, NULL, NULL)`,
		`INSERT INTO messages (account, id, folder, sender_address, subject, received_at, is_draft) VALUES ('demo', 'draft', 'inbox', 'demo@fake.test', 'Draft invoice', '2026-03-31T11:00:00Z', 1)`)
	return fake.New("demo", home, func() time.Time { return now }).Mailbox(), home
}

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
	got, err := m.List(ctx, "inbox", 2, 1, false)
	if err != nil || !reflect.DeepEqual(ids(got), []string{"new", "mid"}) {
		t.Fatalf("ids=%v err=%v", ids(got), err)
	}
	if got[0].Body != nil || got[0].ContentType != nil || got[0].Auth != nil {
		t.Fatal("metadata list carried body or auth")
	}
	want := model.Message{ID: "new", ThreadID: "new", Sender: model.Address{Name: "Bob", Address: "bob@example.test"}, Subject: "Lunch plans", ReceivedAt: time.Date(2026, 3, 31, 8, 0, 0, 0, time.UTC), To: []model.Address{{Address: "demo@fake.test"}}, CC: []model.Address{}, Folders: []string{"inbox"}, Attachments: []string{}, Flag: model.NoFlag, Categories: []string{}}
	got[0].ReceivedAt = got[0].ReceivedAt.UTC()
	if !reflect.DeepEqual(got[0], want) {
		t.Fatalf("message=%+v", got[0])
	}
	for _, call := range []func() error{
		func() error { _, err := m.List(ctx, "nowhere", 25, 0, false); return err },
		func() error { _, err := m.List(ctx, "inbox", 0, 0, false); return err },
		func() error { _, err := m.List(ctx, "inbox", 25, -1, false); return err },
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
	day := func(s string) *time.Time { d, _ := time.Parse("2006-01-02", s); return &d }
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
		{"after and before are inclusive dates", model.SearchQuery{After: day("2026-03-15"), Before: day("2026-03-20")}, 25, []string{"arc", "mid"}},
		{"folder", model.SearchQuery{Text: text("invoice"), Folder: &archive}, 25, []string{"arc"}},
		{"count", model.SearchQuery{Text: text("invoice")}, 2, []string{"draft", "arc"}},
	} {
		got, err := m.Search(ctx, tc.query, tc.count, false)
		if err != nil || !reflect.DeepEqual(ids(got), tc.want) {
			t.Errorf("%s: ids=%v err=%v", tc.name, ids(got), err)
		}
	}
	nowhere := "nowhere"
	if _, err := m.Search(ctx, model.SearchQuery{Folder: &nowhere}, 25, false); err == nil {
		t.Error("unknown folder accepted")
	}
}

func TestSearchByRecipientMatchesToAndCcButNotTheSender(t *testing.T) {
	home := t.TempDir()
	seed(t, home, `INSERT INTO messages (account, id, folder, sender_address, to_addresses, cc_addresses, subject, received_at) VALUES
		('demo', 'to', 'inbox', 'bob@example.test', '["ana@example.test"]', '[]', 'To Ana', '2026-03-01T09:00:00Z'),
		('demo', 'cc', 'inbox', 'bob@example.test', '["carla@example.test"]', '["ana@example.test"]', 'Cc Ana', '2026-03-02T09:00:00Z'),
		('demo', 'sender', 'inbox', 'ana@example.test', '["bob@example.test"]', '[]', 'From Ana', '2026-03-03T09:00:00Z'),
		('demo', 'nobody', 'inbox', 'ana@example.test', '[]', '[]', 'Note to self', '2026-03-04T09:00:00Z')`)
	m := fake.New("demo", home, func() time.Time { return now }).Mailbox()
	to := "ANA@example"
	got, err := m.Search(t.Context(), model.SearchQuery{To: &to}, 25, false)
	if err != nil || !reflect.DeepEqual(ids(got), []string{"cc", "to"}) {
		t.Fatalf("ids=%v err=%v", ids(got), err)
	}
	// An empty value is no filter, as for the other fields and the real providers.
	empty, subject := "", "self"
	got, err = m.Search(t.Context(), model.SearchQuery{To: &empty, Subject: &subject}, 25, false)
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

func TestFoldersMarkTrashAndEveryFolderBelowIt(t *testing.T) {
	m, home := mailbox(t)
	if _, err := m.Folders(context.Background()); err != nil {
		t.Fatal(err)
	}
	seed(t, home, `INSERT INTO folders (account, id, name, parent) VALUES ('demo', 'projects', 'Projects', NULL), ('demo', 'old', 'Old', 'trash'), ('demo', 'older', 'Older', 'old'), ('other', 'theirs', 'Theirs', NULL)`)
	got, err := m.Folders(context.Background())
	want := []model.Folder{{ID: "inbox", Name: "Inbox"}, {ID: "drafts", Name: "Drafts"}, {ID: "sent", Name: "Sent"}, {ID: "archive", Name: "Archive"}, {ID: "trash", Name: "Trash", Trash: true}, {ID: "projects", Name: "Projects"}, {ID: "old", Name: "Old", Trash: true}, {ID: "older", Name: "Older", Trash: true}}
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
	seed(t, home, `INSERT INTO categories (account, name) VALUES ('demo', 'Work'), ('demo', 'Home'), ('other', 'Theirs')`)
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
	if got, err := m.List(ctx, "trash", 25, 0, false); err != nil || !reflect.DeepEqual(ids(got), []string{"mid", "old"}) {
		t.Fatalf("trash=%v err=%v", ids(got), err)
	}
	seed(t, home, `INSERT INTO folders (account, id, name) VALUES ('demo', 'projects', 'Projects')`)
	for _, call := range []func() ([]model.MutationResult, error){
		func() ([]model.MutationResult, error) { return m.Archive(ctx, []string{"new"}) },
		func() ([]model.MutationResult, error) { return m.Move(ctx, []string{"arc"}, "projects") },
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
	if _, err := m.Move(ctx, []string{"new"}, "nowhere"); err == nil {
		t.Fatal("move to an unknown folder accepted")
	}
}

func TestStatusGivesTheAccountAddressWithoutScopes(t *testing.T) {
	home := t.TempDir()
	p := fake.New("demo", home, nil)
	if address, scopes, err := p.Status(context.Background()); err != nil || address != "demo@fake.test" || scopes == nil || len(scopes) != 0 {
		t.Fatalf("address=%q scopes=%v err=%v", address, scopes, err)
	}
	seed(t, home, `INSERT INTO identities (account, address, name) VALUES ('demo', 'me@example.test', 'Demo User')`)
	if address, _, err := p.Status(context.Background()); err != nil || address != "me@example.test" {
		t.Fatalf("address=%q err=%v", address, err)
	}
}

func TestDraftsLandInDraftsFromTheAccountAddress(t *testing.T) {
	m, home := mailbox(t)
	ctx := context.Background()
	seed(t, home, `INSERT INTO identities (account, address, name) VALUES ('demo', 'me@example.test', 'Demo User')`)
	created, err := m.DraftNew(ctx, model.NewDraft{To: []string{"ana@example.test"}, Subject: "Hello", Body: "first"})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := m.DraftReply(ctx, "old", "Thanks")
	if err != nil {
		t.Fatal(err)
	}
	forward, err := m.DraftForward(ctx, "new", []string{"x@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if updated, err := m.DraftUpdate(ctx, created, "second"); err != nil || updated != created {
		t.Fatalf("update=%q err=%v", updated, err)
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
		{[]string{"x@example.test"}, []string{"drafts"}, "Fw: Lunch plans", "<p>Pizza?</p>", model.HTML},
	}
	for i, d := range got {
		to := []string{}
		for _, a := range d.To {
			to = append(to, a.Address)
		}
		if v := (view{to, d.Folders, d.Subject, *d.Body, *d.ContentType}); !reflect.DeepEqual(v, want[i]) || !d.IsDraft || d.Sender != (model.Address{Name: "Demo User", Address: "me@example.test"}) || !d.ReceivedAt.Equal(now) {
			t.Errorf("draft %d=%+v", i, d)
		}
	}
	for _, id := range []string{reply, forward, "old", "gone"} {
		if _, err := m.DraftUpdate(ctx, id, "changed"); err == nil {
			t.Errorf("update of %s accepted", id)
		}
	}
	for _, call := range []func() error{
		func() error { _, err := m.DraftReply(ctx, "gone", "x"); return err },
		func() error { _, err := m.DraftForward(ctx, "theirs", []string{"x@example.test"}); return err },
	} {
		if call() == nil {
			t.Error("draft from a missing message accepted")
		}
	}
}

func TestSendsLandInSentAndRecordKindAndSource(t *testing.T) {
	m, home := mailbox(t)
	ctx := context.Background()
	for _, call := range []func() error{
		func() error {
			return m.SendNew(ctx, model.NewDraft{To: []string{"ana@example.test"}, Subject: "Hello", Body: "hi"})
		},
		func() error { return m.SendReply(ctx, "old", "Thanks") },
		func() error { return m.SendForward(ctx, "new", []string{"x@example.test"}) },
	} {
		if err := call(); err != nil {
			t.Fatal(err)
		}
	}
	if m.SendReply(ctx, "gone", "x") == nil {
		t.Fatal("reply to a missing message accepted")
	}
	sent, err := m.List(ctx, "sent", 25, 0, false)
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
	rows, err := open(t, home).Query(`SELECT subject, origin, coalesce(source_id, '') FROM messages WHERE account = 'demo' AND folder = 'sent' ORDER BY subject`)
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
	if drafts, err := m.List(ctx, "drafts", 25, 0, false); err != nil || len(drafts) != 0 {
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
		if _, _, err := fake.New("demo", home, nil).Status(context.Background()); err != nil {
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
	if sent, err := m.List(ctx, "sent", 25, 0, false); err != nil || len(sent) != 1 {
		t.Fatalf("sent=%v err=%v", ids(sent), err)
	}
}

func TestSeededMailWithoutATimeIsReceivedNow(t *testing.T) {
	m, home := mailbox(t)
	seed(t, home, `INSERT INTO messages (account, id, subject) VALUES ('demo', 'quick', 'No time')`)
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
	list, err := m.List(ctx, "inbox", 25, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	search, err := m.Search(ctx, model.SearchQuery{Text: &invoice}, 25, false)
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
	created, err := m.DraftNew(ctx, model.NewDraft{To: []string{"ana@example.test"}, Subject: "Hello", Body: "first"})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := m.DraftReply(ctx, "old", "Thanks")
	if err != nil {
		t.Fatal(err)
	}
	forward, err := m.DraftForward(ctx, "new", []string{"x@example.test"})
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
		func() error { return m.SendReply(ctx, "old", "Thanks") },
		func() error { return m.SendForward(ctx, "new", []string{"x@example.test"}) },
	} {
		if err := send(); err != nil {
			t.Fatal(err)
		}
	}
	sent, err := m.List(ctx, "sent", 25, 0, false)
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
	for _, s := range []string{before, `INSERT INTO messages (account, id, subject, received_at) VALUES ('demo', 'old', 'Old report', '2026-03-01T09:00:00Z')`} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	m := fake.New("demo", home, func() time.Time { return now }).Mailbox()
	ctx := t.Context()
	list, err := m.List(ctx, "inbox", 25, 0, false)
	if err != nil || !reflect.DeepEqual(threads(list), map[string]string{"old": "old"}) {
		t.Fatalf("list threads=%v err=%v", threads(list), err)
	}
	reply, err := m.DraftReply(ctx, "old", "Thanks")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := m.Get(ctx, []string{reply}, false); err != nil || got[0].ThreadID != "old" {
		t.Fatalf("reply=%+v err=%v", got, err)
	}
	reopened := fake.New("demo", home, func() time.Time { return now }).Mailbox()
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

func TestThreadReturnsTheNewestMessagesOfTheConversationOldestFirstFromEveryFolder(t *testing.T) {
	m, home := mailbox(t)
	ctx := t.Context()
	seed(t, home, `INSERT INTO messages (account, id, folder, sender_address, subject, received_at, is_draft, thread_id) VALUES
		('demo', 'sent-reply', 'sent', 'demo@fake.test', 'Re: Old report', '2026-03-02T09:00:00Z', 0, 'old'),
		('demo', 'binned', 'trash', 'ana@example.test', 'Re: Old report', '2026-03-03T09:00:00+01:00', 0, 'old'),
		('demo', 'reply-draft', 'drafts', 'demo@fake.test', 'Re: Old report', '2026-03-04T09:00:00Z', 1, 'old'),
		('other', 'their-reply', 'inbox', 'ana@example.test', 'Re: Old report', '2026-03-05T09:00:00Z', 0, 'old')`)
	got, older, err := m.Thread(ctx, "old", 25)
	if err != nil || older || !reflect.DeepEqual(ids(got), []string{"old", "sent-reply", "binned", "reply-draft"}) {
		t.Fatalf("ids=%v older=%v err=%v", ids(got), older, err)
	}
	if want := map[string][]string{"old": {"inbox"}, "sent-reply": {"sent"}, "binned": {"trash"}, "reply-draft": {"drafts"}}; !reflect.DeepEqual(folders(got), want) {
		t.Errorf("folders=%v", folders(got))
	}
	if got[0].Body != nil || got[0].Auth != nil || !got[3].IsDraft || got[3].ThreadID != "old" {
		t.Errorf("messages=%+v", got)
	}
	got, older, err = m.Thread(ctx, "old", 2)
	if err != nil || !older || !reflect.DeepEqual(ids(got), []string{"binned", "reply-draft"}) {
		t.Fatalf("cut ids=%v older=%v err=%v", ids(got), older, err)
	}
	if got, older, err := m.Thread(ctx, "old", 4); err != nil || older || len(got) != 4 {
		t.Fatalf("exact count ids=%v older=%v err=%v", ids(got), older, err)
	}
	for _, unknown := range []string{"gone", "theirs"} {
		if got, _, err := m.Thread(ctx, unknown, 25); !errors.Is(err, model.MailboxError("NotFoundError")) || len(got) != 0 {
			t.Errorf("%s: ids=%v err=%v", unknown, ids(got), err)
		}
	}
}
