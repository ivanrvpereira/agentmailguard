package m365_test

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

func TestOnlyEOPAuthenticationResultsWithFailWinsAndWordBoundaries(t *testing.T) {
	tests := []struct {
		name, value string
		want        [5]string
	}{
		{"Authentication-Results-Original", "spf=pass;dkim=pass;dmarc=pass;compauth=pass reason=100", [5]string{}},
		{"ARC-Authentication-Results", "spf=pass;dkim=pass", [5]string{}},
		{" Authentication-Results", "spf=pass", [5]string{}},
		{"AUTHENTICATION-RESULTS", "SPF = Fail ; DKIM=PASS;DMARC =BestGuessPass;COMPAUTH=Pass Reason = 109", [5]string{"fail", "pass", "bestguesspass", "pass", "109"}},
		{"Authentication-Results", "spf=pass;spf=none;spf=fail;spf=pass;dkim=fail;dkim=pass;compauth=pass reason=100;compauth=fail reason=001", [5]string{"fail", "fail", "", "fail", "001"}},
		{"Authentication-Results", "spf=fail (spf=pass dkim=pass) smtp.mailfrom=dmarc=pass;header.dkim=pass;xcompauth=pass;compauth=pass1 reason=100", [5]string{"fail", "", "", "", ""}},
		{"Authentication-Results", "compauth=fail reason=00x1", [5]string{"", "", "", "fail", ""}},
		{"Authentication-Results", "éspf=pass; dkim=passé; compauth=fail reason=100é", [5]string{"", "", "", "fail", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
				jsonResponse(w, map[string]any{"id": "m", "parentFolderId": "inbox-id", "internetMessageHeaders": []any{map[string]string{"name": tt.name, "value": tt.value}}})
			})
			messages, err := c.Mailbox().Get(context.Background(), []string{"m"}, true)
			if err != nil {
				t.Fatal(err)
			}
			a := messages[0].Auth
			if a == nil {
				t.Fatal("headers not marked fetched")
			}
			var got [5]string
			for i, s := range []*string{a.SPF, a.DKIM, a.DMARC, a.CompAuth, a.CompAuthReason} {
				if s != nil {
					got[i] = *s
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %q want %q", got, tt.want)
			}
			if err := a.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
