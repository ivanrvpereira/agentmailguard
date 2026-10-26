package server_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

const draftNewParams = `{"to":["CANARY@example.test"],"subject":"CANARY subject","body":"CANARY body"}`

// jevDaemon is an approvalDaemon whose fake Jev answers each action check with score.
func jevDaemon(t *testing.T, policy string, score float64) (*daemon, *approvalGraph, *nativeAPI) {
	t.Helper()
	api := newNativeAPI()
	api.actionScore = score
	graph := &approvalGraph{}
	event := organizerEvent
	graph.event.Store(&event)
	return graphDaemon(t, policy, writeGrant, graph, api), graph, api
}

func TestJevGetsTheActionFieldsOnly(t *testing.T) {
	d, graph, api := jevDaemon(t, allowAll, 0.08)
	if status, body := accountRPC(t, d.s, "draft.new", "work", draftNewParams); status != 200 || approvalID.MatchString(body) || len(graph.recorded()) != 1 {
		t.Fatalf("%d %s writes=%v", status, body, graph.recorded())
	}
	checks := api.checks()
	if len(checks) != 1 {
		t.Fatalf("checks=%v", checks)
	}
	questions, _ := checks[0]["questions"].(map[string]any)
	question, _ := questions["suspicious"].(map[string]any)
	instructions, _ := question["instructions"].(string)
	if !reflect.DeepEqual(checks[0]["state"], map[string]any{"to": []any{"CANARY@example.test"}, "cc": []any{}, "bcc": []any{}, "subject": "CANARY subject", "body": "CANARY body", "html": false}) ||
		len(questions) != 1 || question["type"] != "noul" ||
		!strings.Contains(instructions, "The agent requested this action: draft.new — creates a draft that the user may send later.") {
		t.Fatalf("check request=%v", checks[0])
	}
}

// The agent gets the normal pending result, never the score or the flag; TestConsoleItemShowsTheJevCheck shows where
// the operator sees them. The approved run asks Jev no second time.
func TestJevFlagHoldsAnAllowedActionUntilApproval(t *testing.T) {
	d, graph, api := jevDaemon(t, allowAll, 0.62)
	status, body := accountRPC(t, d.s, "draft.new", "work", draftNewParams)
	match := approvalID.FindStringSubmatch(body)
	if status != 200 || match == nil || strings.Contains(body, "0.62") || strings.Contains(strings.ToLower(body), "jev") || len(graph.recorded()) != 0 {
		t.Fatalf("%d %s writes=%v", status, body, graph.recorded())
	}
	text, records := auditOf(t, d.home)
	if strings.Contains(text, "CANARY") || strings.Contains(text, "0.62") || !reflect.DeepEqual(records[0]["reasons"], []any{"policy", "jev_flag"}) {
		t.Fatalf("audit=%s", text)
	}
	if status, body := approvalStatus(t, d.s, match[1]); status != 200 || body != `{"state":"pending"}` {
		t.Fatalf("status: %d %s", status, body)
	}
	if err := d.s.Approve(context.Background(), match[1]); err != nil {
		t.Fatal(err)
	}
	if len(api.checks()) != 1 || len(graph.recorded()) != 1 {
		t.Fatalf("approved run: checks=%d writes=%v", len(api.checks()), graph.recorded())
	}
}
