package screening_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/screening"
)

func TestFolderRoundingBoundaryIsStable(t *testing.T) {
	// Python accepts this decimal distribution at the lower inclusive tolerance.
	body := reply(`"folder":{"type":"choice","choice":"a","confidence":0.8,"probabilities":{"a":0.7,"b":0.2,"stay":0.08}}`)
	s := screening.New(screening.Options{LookupEnv: envKey, Credentials: missingStore(), HTTPClient: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return response(body), nil })}})
	asked := &model.OrganizationQuestions{Folder: &model.ChoiceQuestion{Instructions: "Choose", Criteria: map[string]string{"a": "A", "b": "B", "stay": "Stay"}}}
	for range 100 {
		r, err := s.Classify(context.Background(), nil, false, asked)
		if err != nil || r.Organization == nil || r.Error != nil {
			t.Fatalf("valid boundary rejected: %+v %v", r, err)
		}
	}
}
