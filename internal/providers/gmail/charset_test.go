package gmail_test

import (
	"context"
	"encoding/base64"
	"mime"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/gmail"
)

func TestDeclaredCharsetsPreserveBodyHeadersAndReplyQuotes(t *testing.T) {
	// First ten cases are the independent Python oracle, not Go encoder output.
	cases := []struct{ name, charset, data, want string }{
		{"cp1252", "cp1252", "Y2Fm6Q==", "café"},
		{"latin1", "latin1", "Y2Fm6Q==", "café"},
		{"utf-16", "utf-16", "__5jAGEAZgDpAA==", "café"},
		{"utf-16le", "utf-16le", "YwBhAGYA6QA=", "café"},
		{"utf-32", "utf-32", "__4AAGMAAABhAAAAZgAAAOkAAAA=", "café"},
		{"utf-32le", "utf-32le", "YwAAAGEAAABmAAAA6QAAAA==", "café"},
		{"iso-8859-15", "iso-8859-15", "Y2Fm6Q==", "café"},
		{"windows-1252", "windows-1252", "Y2Fm6Q==", "café"},
		{"shift_jis", "shift_jis", "k_qWew==", "日本"},
		{"iso-2022-jp", "iso-2022-jp", "GyRCRnxLXBsoQg==", "日本"},
		{"utf32-big-endian", "utf-32be", "AAAAYwAAAGEAAABmAAAA6Q==", "café"},
		{"utf32-generic-big-bom", "utf-32", "AAD-_wAAAGMAAABhAAAAZgAAAOk=", "café"},
		{"utf32-generic-no-bom", "utf-32", "YwAAAGEAAABmAAAA6QAAAA==", "café"},
		{"utf32-explicit-little-bom", "utf-32le", "__4AAGMAAABhAAAAZgAAAOkAAAA=", "\ufeffcafé"},
		{"utf32-explicit-big-bom", "utf-32be", "AAD-_wAAAGMAAABhAAAAZgAAAOk=", "\ufeffcafé"},
		{"utf32-little-wrong-bom", "utf-32le", "AAD-_wAAAGM=", "\ufffd\ufffd"},
		{"utf32-big-wrong-bom", "utf-32be", "__4AAGMAAAA=", "\ufffd\ufffd"},
		{"utf32-invalid-scalar", "utf-32le", "ANgAAGMAAAA=", "\ufffdc"},
		{"utf32-trailing-byte", "utf-32le", "YwAAAP8=", "c\ufffd"},
		{"latin-controls", "ISO_8859_1", "gOk=", "\u0080é"},
		{"windows-punctuation", "CP1252", "gOk=", "€é"},
		{"latin9-euro", "ISO_8859_15", "pOk=", "€é"},
	}
	for _, alias := range []struct{ charset, data, want string }{
		{"Windows_1252", "Y2Fm6Q==", "café"},
		{"Latin_1", "Y2Fm6Q==", "café"},
		{"iso8859_1", "gOk=", "\u0080é"},
		{"UTF_32", "__4AAGMAAABhAAAAZgAAAOkAAAA=", "café"},
		{"utf32", "__4AAGMAAABhAAAAZgAAAOkAAAA=", "café"},
		{"UTF_32_LE", "YwAAAGEAAABmAAAA6QAAAA==", "café"},
		{"UTF_32_BE", "AAAAYwAAAGEAAABmAAAA6Q==", "café"},
		{"utf-32-le", "YwAAAGEAAABmAAAA6QAAAA==", "café"},
		{"utf-32-be", "AAAAYwAAAGEAAABmAAAA6Q==", "café"},
		{"UTF_16", "__5jAGEAZgDpAA==", "café"},
		{"UTF_16_LE", "YwBhAGYA6QA=", "café"},
		{"utf16", "__5jAGEAZgDpAA==", "café"},
		{"Shift-JIS", "k_qWew==", "日本"},
		{"iso_2022_jp", "GyRCRnxLXBsoQg==", "日本"},
	} {
		cases = append(cases, struct{ name, charset, data, want string }{"alias-" + alias.charset, alias.charset, alias.data, alias.want})
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := base64.URLEncoding.DecodeString(tt.data)
			if err != nil {
				t.Fatal(err)
			}
			encodedWord := "=?" + tt.charset + "?B?" + base64.StdEncoding.EncodeToString(raw) + "?="
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/labels":
					respond(w, map[string]any{})
				case "/messages/m":
					respond(w, message("m", map[string]any{"mimeType": "text/plain", "headers": []map[string]string{
						{"name": "Content-Type", "value": "text/plain; charset=" + tt.charset},
						{"name": "Subject", "value": encodedWord},
						{"name": "From", "value": encodedWord + " <alice@example.test>"},
					}, "body": map[string]any{"data": tt.data}}))
				case "/drafts":
					msg, _ := draftMIME(t, r)
					subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
					if err != nil || subject != "Re: "+tt.want {
						t.Errorf("reply subject=%q; want=%q", subject, "Re: "+tt.want)
					}
					want := "Thanks\n\nOn 2026-09-01 00:00 UTC, alice@example.test wrote:\n> " + tt.want + "\n"
					if got := draftText(t, msg); got != want {
						t.Errorf("reply quote=%q; want=%q", got, want)
					}
					respond(w, map[string]any{"message": map[string]string{"id": "created"}})
				default:
					t.Error("unexpected request")
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			m := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL}).Mailbox()
			got, err := m.Get(ctx, []string{"m"}, true)
			if err != nil || len(got) != 1 || got[0].Body == nil {
				t.Fatal("body read failed", err)
			}
			if *got[0].Body != tt.want || got[0].Subject != tt.want || got[0].Sender.Name != tt.want {
				t.Errorf("body=%q subject=%q sender=%q; want=%q", *got[0].Body, got[0].Subject, got[0].Sender.Name, tt.want)
			}
			if id, err := m.DraftReply(ctx, "m", "Thanks"); err != nil || id != "created" {
				t.Fatal("reply failed", err)
			}
		})
	}
}
