// Package audit appends content-free mutation records to a private JSONL file.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

type Operation string

const (
	Archive         Operation = "archive"
	Trash           Operation = "trash"
	Spam            Operation = "spam"
	Move            Operation = "move"
	Read            Operation = "read"
	Unread          Operation = "unread"
	Flag            Operation = "flag"
	Categorize      Operation = "categorize"
	DraftNew        Operation = "draft_new"
	DraftReply      Operation = "draft_reply"
	DraftForward    Operation = "draft_forward"
	DraftUpdate     Operation = "draft_update"
	DraftDelete     Operation = "draft_delete"
	MailSend        Operation = "mail_send"
	MailReply       Operation = "mail_reply"
	MailForward     Operation = "mail_forward"
	DraftSend       Operation = "draft_send"
	CalendarInvite  Operation = "calendar_invite"
	CalendarUpdate  Operation = "calendar_update"
	CalendarCancel  Operation = "calendar_cancel"
	CalendarRespond Operation = "calendar_respond"
)

// Options contains only operation settings. Folder, category, and draft IDs must
// come from validated mailbox operations, never arbitrary message text. Thread is the conversation of a conversation
// action, an opaque provider ID.
type Options struct {
	Folder        *string
	Flag          *model.FlagState
	Add, Remove   []string // nil: absent; empty non-nil: explicitly empty
	DraftID, Rule *string
	Thread        *string
}
type Log struct {
	path string
	mu   sync.Mutex
}

func New(path string) *Log { return &Log{path: path} }

type result struct {
	ID    string  `json:"id"`
	OK    bool    `json:"ok"`
	Error *string `json:"error"`
}
type record struct {
	Time      string           `json:"time"`
	Account   string           `json:"account"`
	Operation Operation        `json:"operation"`
	Rule      *string          `json:"rule"`
	IDs       []string         `json:"ids"`
	Folder    *string          `json:"folder,omitempty"`
	Flag      *model.FlagState `json:"flag,omitempty"`
	Add       *[]string        `json:"add,omitempty"`
	Remove    *[]string        `json:"remove,omitempty"`
	DraftID   *string          `json:"draft_id,omitempty"`
	Thread    *string          `json:"thread,omitempty"`
	Results   []result         `json:"results"`
}

func (l *Log) Record(ctx context.Context, account string, operation Operation, results []model.MutationResult, options Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch operation {
	case Archive, Trash, Spam, Move, Read, Unread, Flag, Categorize, DraftNew, DraftReply, DraftForward, DraftUpdate:
	case CalendarInvite, CalendarUpdate, CalendarCancel, CalendarRespond, MailSend, MailReply, MailForward, DraftDelete, DraftSend:
		if options.Folder != nil || options.Flag != nil || options.Add != nil || options.Remove != nil || options.DraftID != nil || options.Rule != nil {
			return errors.New("invalid audit options")
		}
	default:
		return errors.New("invalid audit operation")
	}
	// Only an organize action other than flag can act on a conversation.
	if options.Thread != nil && (*options.Thread == "" || !slices.Contains([]Operation{Archive, Trash, Spam, Move, Read, Unread, Categorize}, operation)) {
		return errors.New("invalid audit thread")
	}
	if options.Flag != nil && *options.Flag != model.Flagged && *options.Flag != model.Complete && *options.Flag != model.NoFlag {
		return errors.New("invalid audit flag")
	}
	if !validRule(options.Rule) {
		return errors.New("invalid audit rule")
	}
	for _, r := range results {
		if err := validResultError(r.Error); err != nil {
			return err
		}
	}
	line := record{Time: now(), Account: account, Operation: operation, Rule: options.Rule, Folder: options.Folder, Flag: options.Flag, DraftID: options.DraftID, Thread: options.Thread, IDs: []string{}, Results: []result{}}
	if options.Add != nil {
		line.Add = &options.Add
	}
	if options.Remove != nil {
		line.Remove = &options.Remove
	}
	for _, r := range results {
		line.IDs = append(line.IDs, r.ID)
		line.Results = append(line.Results, result{ID: r.ID, OK: r.OK, Error: r.Error})
	}
	return l.append(ctx, line)
}

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05+00:00") }

func validRule(rule *string) bool {
	if rule == nil || model.ValidRuleName(*rule) {
		return true
	}
	switch *rule {
	case "security:yellow", "security:red", "security:sealed":
		return true
	}
	return false
}

// append writes one JSON line and syncs the private audit file.
func (l *Log) append(ctx context.Context, line any) error {
	bytes, err := json.Marshal(line)
	if err != nil {
		return errors.New("cannot encode audit record")
	}
	bytes = append(bytes, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	folder := filepath.Dir(l.path)
	if err := os.MkdirAll(folder, 0700); err != nil {
		return errors.New("cannot create audit directory")
	}
	if err := os.Chmod(folder, 0700); err != nil {
		return errors.New("cannot secure audit directory")
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return errors.New("cannot open audit file")
	}
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return errors.New("cannot secure audit file")
	}
	if n, err := file.Write(bytes); err != nil || n != len(bytes) {
		return errors.New("cannot append audit record")
	}
	if err := file.Sync(); err != nil {
		return errors.New("cannot sync audit file")
	}
	return nil
}
