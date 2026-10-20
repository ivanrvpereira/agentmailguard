package gmail

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"mime"
	"net/mail"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"golang.org/x/text/encoding/ianaindex"
	textunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/encoding/unicode/utf32"
)

type header struct{ Name, Value string }
type mimePart struct {
	MimeType string
	Filename string
	Headers  []header
	Body     struct {
		Data string
		Size *int64
	}
	Parts []mimePart
}
type wireMessage struct {
	ID, ThreadID, InternalDate, Raw string
	LabelIDs                        []string
	Payload                         mimePart
}

func headerValue(headers []header, name string) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}
func charsetReader(label string, input io.Reader) (io.Reader, error) {
	// Common Python codec spelling families, not the entire Python codec registry.
	key := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(label, "_", ""), "-", ""))
	switch key {
	case "cp1252", "windows1252":
		label = "windows-1252"
	case "latin1", "iso88591":
		label = "iso-8859-1" // Preserve Latin controls; do not use WHATWG aliases.
	case "latin9", "iso885915":
		label = "iso-8859-15"
	case "utf8":
		label = "utf-8"
	case "utf16":
		label = "utf-16"
	case "utf16le":
		label = "utf-16le"
	case "utf16be":
		label = "utf-16be"
	case "shiftjis":
		label = "shift_jis"
	case "iso2022jp":
		label = "iso-2022-jp"
	case "utf32", "utf32le", "utf32be":
		endian, policy := utf32.LittleEndian, utf32.IgnoreBOM
		if key == "utf32be" {
			endian = utf32.BigEndian
		} else if key == "utf32" {
			// Python's generic codec consumes a BOM, otherwise using native byte order.
			policy = utf32.UseBOM
			if binary.NativeEndian.Uint32([]byte{1, 0, 0, 0}) != 1 {
				endian = utf32.BigEndian
			}
		}
		return utf32.UTF32(endian, policy).NewDecoder().Reader(input), nil
	}
	encoding, err := ianaindex.MIME.Encoding(label)
	if err != nil || encoding == nil {
		return nil, failure("Gmail", "ValueError")
	}
	return encoding.NewDecoder().Reader(input), nil
}
func decoded(s string) string {
	decoder := mime.WordDecoder{CharsetReader: charsetReader}
	v, err := decoder.DecodeHeader(s)
	if err != nil {
		return s
	}
	return v
}
func addresses(headers []header, name string) []model.Address {
	parser := mail.AddressParser{WordDecoder: &mime.WordDecoder{CharsetReader: charsetReader}}
	values, err := parser.ParseList(headerValue(headers, name))
	out := []model.Address{}
	if err != nil {
		return out
	}
	for _, a := range values {
		out = append(out, model.Address{Name: a.Name, Address: a.Address})
	}
	return out
}
func walk(p *mimePart, visit func(*mimePart, bool) error) error {
	type item struct {
		p        *mimePart
		attached bool
		depth    int
	}
	todo := []item{{p: p}}
	count := 0
	for len(todo) > 0 {
		last := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		count++
		if last.depth > 100 || count > 100000 {
			return failure("Gmail", "ValueError")
		}
		p := last.p
		attached := last.attached || p.Filename != "" || strings.HasPrefix(strings.ToLower(strings.TrimSpace(headerValue(p.Headers, "content-disposition"))), "attachment") || strings.EqualFold(p.MimeType, "message/rfc822")
		if err := visit(p, attached); err != nil {
			return err
		}
		for i := len(p.Parts) - 1; i >= 0; i-- {
			todo = append(todo, item{&p.Parts[i], attached, last.depth + 1})
		}
	}
	return nil
}
func decode64(s string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return nil, failure("Gmail", "ValueError")
	}
	return b, nil
}
func bodyText(payload *mimePart, plainFirst bool) (string, model.ContentType, error) {
	// Missing/null payloads decode to a zero value, not a fetched-empty MIME body.
	if payload == nil {
		return "", "", failure("Gmail", "ValueError")
	}
	mediaType, _, err := mime.ParseMediaType(payload.MimeType)
	if err != nil || !strings.Contains(mediaType, "/") {
		return "", "", failure("Gmail", "ValueError")
	}
	found := map[string]*mimePart{}
	err = walk(payload, func(p *mimePart, attached bool) error {
		kind := strings.ToLower(p.MimeType)
		if !attached && (kind == "text/plain" || kind == "text/html") && found[kind] == nil {
			found[kind] = p
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	order := []string{"text/html", "text/plain"}
	if plainFirst {
		order = []string{"text/plain", "text/html"}
	}
	var p *mimePart
	kind := model.Text
	for _, s := range order {
		if found[s] != nil {
			p = found[s]
			if s == "text/html" {
				kind = model.HTML
			}
			break
		}
	}
	if p == nil {
		return "", kind, nil
	}
	if p.Body.Data == "" && (p.Body.Size == nil || *p.Body.Size != 0) {
		return "", "", failure("Gmail", "ValueError")
	}
	raw, err := decode64(p.Body.Data)
	if err != nil {
		return "", "", err
	}
	_, params, _ := mime.ParseMediaType(headerValue(p.Headers, "content-type"))
	label := strings.ToLower(params["charset"])
	if label != "" && label != "utf-8" && label != "utf8" {
		if reader, e := charsetReader(label, bytes.NewReader(raw)); e == nil {
			converted, e := io.ReadAll(io.LimitReader(reader, responseLimit+1))
			if e != nil || len(converted) > responseLimit {
				return "", "", failure("Gmail", "ValueError")
			}
			raw = converted
		}
	}
	valid, err := textunicode.UTF8.NewDecoder().Bytes(raw)
	if err != nil {
		return "", "", failure("Gmail", "ValueError")
	}
	return string(valid), kind, nil
}

var commentRE = regexp.MustCompile(`\([^()]*\)`)
var resultRE = regexp.MustCompile(`(?i)(spf|dkim|dmarc)\s*=\s*([a-z]+)`)

func authResults(headers []header) *model.AuthResults {
	out := &model.AuthResults{}
	rank := func(s string) int {
		if s == "fail" {
			return 2
		}
		if s == "pass" {
			return 0
		}
		return 1
	}
	found := map[string]string{}
	for _, h := range headers {
		if !strings.EqualFold(h.Name, "Authentication-Results") || !strings.EqualFold(strings.TrimSpace(strings.SplitN(h.Value, ";", 2)[0]), "mx.google.com") {
			continue
		}
		text := commentRE.ReplaceAllString(h.Value, " ")
		for _, m := range resultRE.FindAllStringSubmatchIndex(text, -1) {
			if m[0] > 0 {
				r, _ := utf8.DecodeLastRuneInString(text[:m[0]])
				if unicode.IsLetter(r) || unicode.IsNumber(r) || strings.ContainsRune("_.=-", r) {
					continue
				}
			}
			if m[1] < len(text) {
				r, _ := utf8.DecodeRuneInString(text[m[1]:])
				if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' {
					continue
				}
			}
			method, value := strings.ToLower(text[m[2]:m[3]]), strings.ToLower(text[m[4]:m[5]])
			old, ok := found[method]
			if !ok || rank(value) > rank(old) {
				found[method] = value
			}
		}
		break
	}
	if s, ok := found["spf"]; ok {
		out.SPF = &s
	}
	if s, ok := found["dkim"]; ok {
		out.DKIM = &s
	}
	if s, ok := found["dmarc"]; ok {
		out.DMARC = &s
	}
	return out
}

var millisecondsRE = regexp.MustCompile(`^-?[0-9]+$`)

func timestamp(s string) (time.Time, error) {
	if !millisecondsRE.MatchString(s) {
		return time.Time{}, failure("Gmail", "InvalidMetadata")
	}
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, failure("Gmail", "InvalidMetadata")
	}
	t := time.UnixMilli(ms).UTC()
	if t.Year() < 1 || t.Year() > 9999 {
		return time.Time{}, failure("Gmail", "InvalidMetadata")
	}
	return t, nil
}
func contains(a []string, s string) bool {
	return slices.Contains(a, s)
}
func parseMessage(data map[string]json.RawMessage, labels map[string]string, body bool) (model.Message, error) {
	raw, _ := json.Marshal(data)
	var v wireMessage
	if json.Unmarshal(raw, &v) != nil || strings.TrimSpace(v.ID) == "" {
		return model.Message{}, failure("Gmail", "ValueError")
	}
	stamp := v.InternalDate
	if stamp == "" {
		stamp = "0"
	}
	received, err := timestamp(stamp)
	if err != nil {
		return model.Message{}, err
	}
	m := model.Message{ID: v.ID, Subject: decoded(headerValue(v.Payload.Headers, "subject")), ReceivedAt: received, To: addresses(v.Payload.Headers, "to"), CC: addresses(v.Payload.Headers, "cc"), Folders: []string{}, Attachments: []string{}, Categories: []string{}, IsDraft: contains(v.LabelIDs, "DRAFT"), IsRead: !contains(v.LabelIDs, "UNREAD"), Flag: model.NoFlag}
	if a := addresses(v.Payload.Headers, "from"); len(a) > 0 {
		m.Sender = a[0]
	}
	if contains(v.LabelIDs, "STARRED") {
		m.Flag = model.Flagged
	}
	for _, id := range v.LabelIDs {
		if id != "UNREAD" && id != "STARRED" {
			m.Folders = append(m.Folders, id)
		}
		if name, ok := labels[id]; ok {
			m.Categories = append(m.Categories, name)
		}
	}
	err = walk(&v.Payload, func(p *mimePart, _ bool) error {
		if p.Filename != "" {
			m.Attachments = append(m.Attachments, p.Filename)
		}
		return nil
	})
	if err != nil {
		return model.Message{}, err
	}
	if body {
		s, kind, err := bodyText(&v.Payload, false)
		if err != nil {
			return model.Message{}, err
		}
		m.Body = &s
		m.ContentType = &kind
		m.Auth = authResults(v.Payload.Headers)
	}
	return m, nil
}
