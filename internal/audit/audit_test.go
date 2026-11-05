package audit_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/audit"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

func TestAuditAppendsOnlyFixedFieldsWithExplicitNullRule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "audit.jsonl")
	log := audit.New(path)
	failure := "Graph error 404 ErrorItemNotFound"
	results := []model.MutationResult{{ID: "m1", OK: true}, {ID: "gone", OK: false, Error: &failure}}
	if err := log.Record(context.Background(), "work", "archive", results, audit.Options{}); err != nil {
		t.Fatal(err)
	}
	rule := "finance"
	if err := log.Record(context.Background(), "work", "categorize", results, audit.Options{Rule: &rule, Add: []string{"Later"}, Remove: []string{}}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	var lines []map[string]any
	for scanner.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("lines=%d", len(lines))
	}
	keys := []string{}
	for key := range lines[0] {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"account", "ids", "operation", "results", "rule", "time"}) || lines[0]["rule"] != nil {
		t.Fatalf("unexpected fields: %v", lines[0])
	}
	if _, err := time.Parse(time.RFC3339, lines[0]["time"].(string)); err != nil {
		t.Fatal(err)
	}
	if lines[1]["rule"] != "finance" || !reflect.DeepEqual(lines[1]["remove"], []any{}) {
		t.Fatalf("lost settings: %v", lines[1])
	}
	wantResults := []any{map[string]any{"id": "m1", "ok": true, "error": nil}, map[string]any{"id": "gone", "ok": false, "error": failure}}
	if !reflect.DeepEqual(lines[0]["results"], wantResults) {
		t.Fatalf("results=%v", lines[0]["results"])
	}
	for name, mode := range map[string]os.FileMode{path: 0600, filepath.Dir(path): 0700} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("not private: %s", name)
		}
	}
}

func TestCalendarAuditRecordsHaveOnlyStandardFields(t *testing.T) {
	for _, op := range []audit.Operation{audit.CalendarInvite, audit.CalendarUpdate, audit.CalendarCancel, audit.CalendarRespond} {
		t.Run(string(op), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audit.jsonl")
			if err := audit.New(path).Record(context.Background(), "work", op, []model.MutationResult{{ID: "e1", OK: true}}, audit.Options{}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var record map[string]any
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			if len(record) != 6 || record["operation"] != string(op) || record["account"] != "work" || record["rule"] != nil || !reflect.DeepEqual(record["ids"], []any{"e1"}) || !reflect.DeepEqual(record["results"], []any{map[string]any{"id": "e1", "ok": true, "error": nil}}) {
				t.Fatalf("unexpected record: %v", record)
			}
			if _, err := time.Parse(time.RFC3339, record["time"].(string)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCalendarAuditRefusesContentBearingOptions(t *testing.T) {
	text := "CANARY@example.test"
	flag := model.Flagged
	rule := "finance"
	for _, op := range []audit.Operation{audit.CalendarInvite, audit.CalendarUpdate, audit.CalendarCancel, audit.CalendarRespond} {
		for name, opts := range map[string]audit.Options{"add": {Add: []string{text}}, "remove": {Remove: []string{text}}, "folder": {Folder: &text}, "draft": {DraftID: &text}, "flag": {Flag: &flag}, "rule": {Rule: &rule}} {
			t.Run(string(op)+"/"+name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "audit.jsonl")
				if err := audit.New(path).Record(context.Background(), "work", op, []model.MutationResult{{ID: "e1", OK: true}}, opts); err == nil {
					t.Error("accepted non-calendar metadata")
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Error("rejected metadata created an audit file")
				}
			})
		}
	}
}

func TestAuditPreservesNormalizedOrganizerFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	failure, rule := "failed", "finance"
	if err := audit.New(path).Record(context.Background(), "work", audit.Archive, []model.MutationResult{{ID: "m1", Error: &failure}}, audit.Options{Rule: &rule}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record struct{ Results []model.MutationResult }
	if err := json.Unmarshal(data, &record); err != nil || !reflect.DeepEqual(record.Results, []model.MutationResult{{ID: "m1", Error: &failure}}) {
		t.Fatalf("normalized result changed: %v %v", record.Results, err)
	}
}

func TestAuditSecurityRulesAreFixedAndPreserved(t *testing.T) {
	for _, rule := range []string{"security:yellow", "security:red", "security:sealed", "finance"} {
		t.Run(rule, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audit.jsonl")
			if err := audit.New(path).Record(context.Background(), "work", audit.Categorize, []model.MutationResult{{ID: "m1", OK: true}}, audit.Options{Rule: &rule}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var record struct{ Rule string }
			if err := json.Unmarshal(data, &record); err != nil || record.Rule != rule {
				t.Fatalf("audit rule changed: %q %v", record.Rule, err)
			}
		})
	}
	for _, rule := range []string{"security:green", "security:error", "security:purple", "security:red:extra"} {
		path := filepath.Join(t.TempDir(), "audit.jsonl")
		if err := audit.New(path).Record(context.Background(), "work", audit.Categorize, nil, audit.Options{Rule: &rule}); err == nil {
			t.Errorf("unsupported audit rule accepted: %s", rule)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("rejected audit rule created a file")
		}
	}
}

func TestAuditRejectsUnvalidatedMetadataWithoutAppending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	log := audit.New(path)
	if err := log.Record(context.Background(), "work", "read", nil, audit.Options{}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	flag := model.FlagState("RAW CONTENT CANARY")
	rule := "RAW CONTENT CANARY"
	failure := "RAW CONTENT CANARY"
	for _, input := range []struct {
		operation audit.Operation
		options   audit.Options
		results   []model.MutationResult
	}{
		{operation: "RAW CONTENT CANARY"},
		{operation: "flag", options: audit.Options{Flag: &flag}},
		{operation: "archive", options: audit.Options{Rule: &rule}},
		{operation: "archive", results: []model.MutationResult{{ID: "m1", Error: &failure}}},
		// Only an organize action other than flag acts on a conversation, and its thread is never empty.
		{operation: "flag", options: audit.Options{Thread: new("t1")}},
		{operation: "draft_new", options: audit.Options{Thread: new("t1")}},
		{operation: "calendar_invite", options: audit.Options{Thread: new("t1")}},
		{operation: "archive", options: audit.Options{Thread: new("")}},
	} {
		if err := log.Record(context.Background(), "work", input.operation, input.results, input.options); err == nil {
			t.Error("unvalidated metadata accepted")
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("rejected record changed the audit log")
	}
}

func TestConcurrentAuditRecordsRemainWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	log := audit.New(path)
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			if err := log.Record(context.Background(), "work", "read", []model.MutationResult{{ID: "m1", OK: true}}, audit.Options{}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	count := 0
	for scanner.Scan() {
		if !json.Valid(scanner.Bytes()) {
			t.Fatal("torn audit record")
		}
		count++
	}
	if err := scanner.Err(); err != nil || count != 30 {
		t.Fatalf("count=%d error=%v", count, err)
	}
}

func TestAuditAcceptsOnlyExactPolicyResultCodes(t *testing.T) {
	for _, code := range []string{"pending", "refused by policy: mail_trash", "refused by policy: calendar_invite"} {
		path := filepath.Join(t.TempDir(), "audit.jsonl")
		if err := audit.New(path).Record(context.Background(), "work", audit.Trash, []model.MutationResult{{ID: "m1", Error: &code}}, audit.Options{}); err != nil {
			t.Errorf("%q refused: %v", code, err)
		}
	}
	for _, code := range []string{"pending CANARY", "Pending", "refused by policy: CANARY", "refused by policy: mail_trash CANARY", "refused by policy: "} {
		path := filepath.Join(t.TempDir(), "audit.jsonl")
		if err := audit.New(path).Record(context.Background(), "work", audit.Trash, []model.MutationResult{{ID: "m1", Error: &code}}, audit.Options{}); err == nil {
			t.Errorf("%q accepted", code)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("rejected code created a file")
		}
	}
}

func TestPolicyRecordsAcceptSourceAndJevReasons(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	for _, reasons := range [][]string{{"policy", "jev_flag"}, {"policy", "sealed", "jev_error"}, {"policy", "red"}} {
		d := audit.Decision{Operation: "draft.forward", Key: "mail_draft", Decision: "approve", Reasons: reasons, ApprovalID: &id}
		if err := audit.New(path).Policy(context.Background(), "work", d); err != nil {
			t.Fatalf("%v: %v", reasons, err)
		}
	}
}

func TestPolicyRecordsRefuseUnknownValues(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	valid := audit.Decision{Operation: "mail.trash", Key: "mail_trash", Decision: "approve", Reasons: []string{"policy", "sealed"}, ApprovalID: &id}
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := audit.New(path).Policy(context.Background(), "work", valid); err != nil {
		t.Fatal(err)
	}
	if err := audit.New(path).Approval(context.Background(), "work", "mail.trash", "created", id); err != nil {
		t.Fatal(err)
	}
	bad := "CANARY"
	for name, d := range map[string]audit.Decision{
		"operation":   {Operation: "CANARY", Key: "mail_trash", Decision: "deny", Reasons: []string{"policy"}},
		"key":         {Operation: "mail.trash", Key: "CANARY", Decision: "deny", Reasons: []string{"policy"}},
		"decision":    {Operation: "mail.trash", Key: "mail_trash", Decision: "CANARY", Reasons: []string{"policy"}},
		"reason":      {Operation: "mail.trash", Key: "mail_trash", Decision: "deny", Reasons: []string{"CANARY"}},
		"approval":    {Operation: "mail.trash", Key: "mail_trash", Decision: "approve", Reasons: []string{"policy"}, ApprovalID: &bad},
		"no approval": {Operation: "mail.trash", Key: "mail_trash", Decision: "approve", Reasons: []string{"policy"}},
		"rule":        {Operation: "mail.trash", Key: "mail_trash", Decision: "deny", Reasons: []string{"policy"}, Rule: &bad},
	} {
		if err := audit.New(path).Policy(context.Background(), "work", d); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	for _, args := range [][2]string{{"CANARY", "created"}, {"mail.trash", "CANARY"}} {
		if err := audit.New(path).Approval(context.Background(), "work", args[0], args[1], id); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	if err := audit.New(path).Approval(context.Background(), "work", "mail.trash", "created", bad); err == nil {
		t.Error("invalid approval ID accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Count(string(data), "\n") != 2 || strings.Contains(string(data), "CANARY") {
		t.Fatalf("audit=%s %v", data, err)
	}
}
