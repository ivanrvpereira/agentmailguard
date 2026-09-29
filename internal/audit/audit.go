// Package audit appends content-free mutation records to a private JSONL file.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
)

type Operation string

const (
	Archive      Operation = "archive"
	Trash        Operation = "trash"
	Move         Operation = "move"
	Read         Operation = "read"
	Unread       Operation = "unread"
	Flag         Operation = "flag"
	Categorize   Operation = "categorize"
	DraftNew     Operation = "draft_new"
	DraftReply   Operation = "draft_reply"
	DraftForward Operation = "draft_forward"
	DraftUpdate  Operation = "draft_update"
)

// Options contains only operation settings. Folder, category, and draft IDs must
// come from validated mailbox operations, never arbitrary message text.
type Options struct {
	Folder        *string
	Flag          *model.FlagState
	Add, Remove   []string // nil: absent; empty non-nil: explicitly empty
	DraftID, Rule *string
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
	Results   []result         `json:"results"`
}

func (l *Log) Record(ctx context.Context, account string, operation Operation, results []model.MutationResult, options Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch operation {
	case Archive, Trash, Move, Read, Unread, Flag, Categorize, DraftNew, DraftReply, DraftForward, DraftUpdate:
	default:
		return errors.New("invalid audit operation")
	}
	if options.Flag != nil && *options.Flag != model.Flagged && *options.Flag != model.Complete && *options.Flag != model.NoFlag {
		return errors.New("invalid audit flag")
	}
	if options.Rule != nil && !model.ValidRuleName(*options.Rule) {
		switch *options.Rule {
		case "security:yellow", "security:red", "security:sealed":
		default:
			return errors.New("invalid audit rule")
		}
	}
	for _, r := range results {
		if err := model.ValidateFailureText(r.Error); err != nil {
			return err
		}
	}
	line := record{Time: time.Now().UTC().Format("2006-01-02T15:04:05+00:00"), Account: account, Operation: operation, Rule: options.Rule, Folder: options.Folder, Flag: options.Flag, DraftID: options.DraftID, IDs: []string{}, Results: []result{}}
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
