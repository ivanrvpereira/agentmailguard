// Package content renders untrusted mail without network or resource access.
// Rendered text is still untrusted; only its display surface is reduced.
package content

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
	"golang.org/x/net/idna"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const MaxChars = 20000
const MaxDepth = 200

type Rendered struct {
	Markdown  string
	Links     map[string]string // L1 -> original URL; IDs follow document order.
	Truncated bool
}

var ansi = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)?")
var pieces = regexp.MustCompile("(?i)\x00[^\x00]*\x00|[a-z0-9+.-]*://[^\\s\\p{Z}\\x{0085}<>\"'‹›\x00]*|\\[[\\s\\p{Z}]*L[\\s\\p{Z}]*[\\p{Nd}]+[\\s\\p{Z}]*\\]")
var hostChars = regexp.MustCompile(`^[a-z0-9._:-]+$`)
var marker = regexp.MustCompile(`\[[^\[\]\n]{0,500}\]\[L\d+\]|\[L\d+\]`)

// Hangul fillers are letters that render blank; NFKC maps U+3164 and U+FFA0 to U+1160, which is still blank.
// One basic variation selector per character is legitimate (emoji presentation); a run of them is a byte carrier.
func stripInvisible(s string) string {
	var b strings.Builder
	selector := false
	for _, r := range ansi.ReplaceAllString(s, "") {
		variation := r >= 0xfe00 && r <= 0xfe0f
		if r != '\n' && r != '\t' && (unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) || (r >= 0xe0100 && r <= 0xe01ef) || r == 0x115f || r == 0x1160 || r == 0x3164 || r == 0xffa0 || (variation && selector)) {
			continue
		}
		selector = variation
		b.WriteRune(r)
	}
	return b.String()
}
func cleanText(s string) string {
	return strings.NewReplacer("<", "‹", ">", "›").Replace(norm.NFKC.String(stripInvisible(s)))
}

// Preserve raw percent escapes like urllib.urlsplit; net/url otherwise decodes
// the host early and rejects unrelated malformed percent escapes in paths.
func splitURL(s string) (*url.URL, error) {
	u, err := url.Parse(strings.ReplaceAll(s, "%", "%25"))
	if err != nil {
		return nil, err
	}
	u.RawQuery = strings.ReplaceAll(u.RawQuery, "%25", "%")
	netloc := strings.NewReplacer(":", "", "@", "", "#", "", "?", "").Replace(u.Host)
	if strings.ContainsAny(norm.NFKC.String(netloc), "/?#@:") {
		return nil, errors.New("invalid URL")
	}
	return u, nil
}

func webURL(s string) string {
	s = strings.Join(strings.Fields(stripInvisible(s)), "")
	u, err := splitURL(s)
	if err != nil || (strings.ToLower(u.Scheme) != "http" && strings.ToLower(u.Scheme) != "https") || u.Hostname() == "" {
		return ""
	}
	return s
}
func finish(s string, registry map[string]string) string {
	s = pieces.ReplaceAllStringFunc(s, func(part string) string {
		if part[0] == '[' {
			return strings.NewReplacer("[", `\[`, "]", `\]`).Replace(part)
		}
		target, suffix := "", ""
		if part[0] == 0 {
			target = part[1 : len(part)-1]
		} else {
			trimmed := strings.TrimRight(part, ".,;:!?)]}*`")
			// :// itself is always part of the URL candidate.
			if len(trimmed) < strings.Index(part, "://")+3 {
				trimmed = part
			}
			suffix = part[len(trimmed):]
			target = webURL(trimmed)
		}
		if target == "" || registry == nil {
			return "[link removed]" + suffix
		}
		id, ok := registry[target]
		if !ok {
			id = fmt.Sprintf("L%d", len(registry)+1)
			registry[target] = id
		}
		return "[" + id + "]" + suffix
	})
	// HTML strips each text node alone; a selector run split across elements only joins here.
	s = stripInvisible(s)
	var b strings.Builder
	space, newlines := false, 0
	for _, r := range s {
		if r == '\n' {
			space = false
			if b.Len() > 0 && newlines < 2 {
				b.WriteRune(r)
				newlines++
			}
			continue
		}
		if unicode.IsSpace(r) {
			if newlines == 0 && b.Len() > 0 {
				space = true
			}
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
		newlines = 0
	}
	return strings.TrimSpace(b.String())
}

// CleanSubject cleans a single line, removes URLs and escapes forged link markers. It never truncates.
func CleanSubject(subject string) string {
	return finish(strings.Join(strings.Fields(cleanText(subject)), " "), nil)
}

// RenderBody distinguishes a missing body from a fetched empty body. Unknown content types fail closed.
func RenderBody(body *string, kind model.ContentType) (Rendered, error) {
	if body == nil || (kind != model.Text && kind != model.HTML) {
		return Rendered{}, errors.New("render ValueError")
	}
	var text string
	if kind == model.HTML {
		var err error
		text, err = htmlMarkdown(*body)
		if err != nil {
			return Rendered{}, err
		}
	} else {
		text = cleanText(*body)
	}
	registry := map[string]string{}
	md := finish(text, registry)
	runes := []rune(md)
	truncated := len(runes) > MaxChars
	if truncated {
		cut := len(string(runes[:MaxChars]))
		for _, m := range marker.FindAllStringIndex(md, -1) {
			if m[1] > cut {
				if m[0] < cut {
					cut = m[0]
				}
				break
			}
		}
		md = strings.TrimRightFunc(md[:cut], unicode.IsSpace)
	}
	links := map[string]string{}
	for u, id := range registry {
		if strings.Contains(md, "["+id+"]") {
			links[id] = u
		}
	}
	return Rendered{md, links, truncated}, nil
}

// Domains exposes hosts only, never credentials, paths, query strings or fragments.
func (r Rendered) Domains() map[string]string {
	result := make(map[string]string, len(r.Links))
	for id, target := range r.Links {
		host := "invalid-host"
		if u, err := splitURL(target); err == nil {
			name := norm.NFKC.String(cases.Fold().String(u.Hostname()))
			ascii, err := idna.Punycode.ToASCII(name)
			validLabels := true
			for _, label := range strings.Split(strings.TrimSuffix(ascii, "."), ".") {
				if len(label) == 0 || len(label) > 63 {
					validLabels = false
				}
			}
			if err == nil && hostChars.MatchString(ascii) && validLabels {
				host = ascii
			}
		}
		result[id] = host
	}
	return result
}
