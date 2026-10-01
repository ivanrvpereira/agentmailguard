package screening_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/screening"
)

type fakeStore struct {
	credentials.Store
	load func(context.Context) (string, error)
}

func (s fakeStore) LoadOpenRouterKey(ctx context.Context) (string, error) { return s.load(ctx) }
func missingStore() fakeStore {
	return fakeStore{load: func(context.Context) (string, error) { return "", credentials.ErrNotFound }}
}
func envKey(string) string { return " synthetic-key " }
func TestRealJevProtocol(t *testing.T) {
	data, err := os.ReadFile("../content/testdata/reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Jev struct {
			Model     string
			Questions map[string]any
		}
	}
	if err := json.Unmarshal(data, &oracle); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"subject": "SYNTHETIC", "body": "hello"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/systemone" || r.Header.Get("Authorization") != "Bearer synthetic-key" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("incorrect Jev request")
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request) != 3 || request["model"] != oracle.Jev.Model || !reflect.DeepEqual(request["questions"], oracle.Jev.Questions) || !reflect.DeepEqual(request["state"], state) {
			t.Errorf("wire request differs from Python: %v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev","usage":{},"answers":{"injection":{"type":"noul","noul":0.35},"account_security":{"type":"noul","noul":0.1}}}`))
	}))
	defer server.Close()
	s := screening.New(screening.Options{HTTPClient: server.Client(), Endpoint: server.URL + "/api/v1/systemone", Credentials: missingStore(), LookupEnv: envKey})
	var _ model.Screener = s
	result, err := s.Screen(context.Background(), state)
	if err != nil || result.Verdict != model.Yellow || result.Score == nil || *result.Score != .35 || result.Error != nil {
		t.Fatalf("result: %+v %v", result, err)
	}
}
