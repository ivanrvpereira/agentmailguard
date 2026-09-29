package screening_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
	"github.com/ivanrvpereira/agentmailguard-go/internal/rules"
	"github.com/ivanrvpereira/agentmailguard-go/internal/screening"
)

const validAnswers = `"injection":{"type":"noul","noul":0.1},"account_security":{"type":"noul","noul":0.1},"folder":{"type":"choice","choice":"finance","confidence":0.7,"probabilities":{"finance":0.8,"stay":0.2}},"category:urgent":{"type":"noul","noul":0.9}`

func reply(answers string) string { return `{"model":"jev","usage":{},"answers":{` + answers + `}}` }
func orgQuestions() *model.OrganizationQuestions {
	return &model.OrganizationQuestions{Folder: &model.ChoiceQuestion{Instructions: screening.FolderInstructions, Criteria: map[string]string{"finance": "Invoices", "stay": "Leave here"}}, Categories: map[string]string{"urgent": "Needs attention"}}
}
func TestOrganizationWireAndIndependentValidation(t *testing.T) {
	cases := []struct {
		name, answers string
		security      bool
		verdict       model.Verdict
		org           bool
		failure       string
	}{
		{"both", validAnswers, true, model.Green, true, ""},
		{"org only", validAnswers, false, "", true, ""},
		{"invalid injection", strings.Replace(validAnswers, `"noul":0.1`, `"noul":1.5`, 1), true, model.Error, true, ""},
		{"invalid folder choice", strings.Replace(validAnswers, `"choice":"finance"`, `"choice":"attacker"`, 1), true, model.Green, false, "invalid answer folder"},
		{"extra folder option", strings.Replace(validAnswers, `"stay":0.2`, `"stay":0.1,"other":0.1`, 1), true, model.Green, false, "invalid answer folder"},
		{"missing folder option", strings.Replace(validAnswers, `,"stay":0.2`, "", 1), true, model.Green, false, "invalid answer folder"},
		{"bad sum", strings.Replace(validAnswers, `"stay":0.2`, `"stay":0.1`, 1), true, model.Green, false, "invalid answer folder"},
		{"sum tolerance", strings.Replace(validAnswers, `"stay":0.2`, `"stay":0.19`, 1), true, model.Green, true, ""},
		{"bad confidence", strings.Replace(validAnswers, `"confidence":0.7`, `"confidence":-0.1`, 1), true, model.Green, false, "invalid answer folder"},
		{"bad category", strings.Replace(validAnswers, `"noul":0.9`, `"noul":-0.1`, 1), true, model.Green, false, "invalid answer category"},
		{"future category type", strings.Replace(validAnswers, `"category:urgent":{"type":"noul"`, `"category:urgent":{"type":"future"`, 1), true, model.Green, false, "invalid answer category"},
		{"schema failure", strings.Replace(validAnswers, `"confidence":0.7`, `"confidence":"SECRET"`, 1), true, model.Error, false, "ValidationError"},
		{"org only schema failure", strings.Replace(validAnswers, `"noul":0.9`, `"noul":null`, 1), false, model.Error, false, "ValidationError"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Questions map[string]map[string]any `json:"questions"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				q := request.Questions
				if (q["injection"] != nil) != c.security {
					t.Error("security question selection")
				}
				if q["folder"]["type"] != "choice" || q["folder"]["instructions"] != screening.FolderInstructions || q["category:urgent"]["type"] != "noul" || q["category:urgent"]["instructions"] != "Needs attention" {
					t.Error("organization wire")
				}
				if _, ok := q["category:urgent"]["criteria"]; ok {
					t.Error("category criteria should be omitted")
				}
				_, _ = w.Write([]byte(reply(c.answers)))
			}))
			defer server.Close()
			s := screening.New(screening.Options{HTTPClient: server.Client(), Endpoint: server.URL, LookupEnv: envKey, Credentials: missingStore()})
			result, err := s.Classify(context.Background(), map[string]any{}, c.security, orgQuestions())
			if err != nil {
				t.Fatal(err)
			}
			if c.verdict == "" {
				if result.Screening != nil {
					t.Fatal("unexpected screening")
				}
			} else if result.Screening == nil || result.Screening.Verdict != c.verdict {
				t.Fatalf("screening: %+v", result.Screening)
			}
			if (result.Organization != nil) != c.org {
				t.Fatalf("organization: %+v", result)
			}
			if c.org {
				if result.Organization.Folder.Choice != "finance" || result.Organization.Categories["urgent"] != .9 {
					t.Fatal(result.Organization)
				}
			}
			if c.failure == "" {
				if result.Error != nil {
					t.Fatal(*result.Error)
				}
			} else if result.Error == nil || *result.Error != c.failure {
				t.Fatalf("error: %+v", result)
			}
		})
	}
}
func TestBuildOrganizationQuestions(t *testing.T) {
	if screening.OrganizationQuestions(rules.Rules{}) != nil {
		t.Fatal("empty rules should ask nothing")
	}
	q := screening.OrganizationQuestions(rules.Rules{Stay: "Custom stay", Rules: []rules.Rule{{Kind: rules.Folder, Name: "finance", Instructions: "Invoices"}, {Kind: rules.Category, Name: "urgent", Instructions: "Needs attention"}}})
	if q == nil || q.Folder == nil || q.Folder.Instructions != "Where does this email belong? Choose the option that fits it best." || q.Folder.Criteria["stay"] != "Custom stay" || q.Folder.Criteria["finance"] != "Invoices" || q.Categories["urgent"] != "Needs attention" {
		t.Fatal(q)
	}
}
