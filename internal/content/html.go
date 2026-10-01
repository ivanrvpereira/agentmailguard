package content

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

var voidTags = words("area base br col embed hr img input link meta param source track wbr")
var dropTags = words("script style head title meta link noscript template iframe object embed frame frameset applet")
var blocks = words("p blockquote article div section ol ul li dl dt dd table thead tbody tfoot tr td th h1 h2 h3 h4 h5 h6")
var tableBlocks = words("table thead tbody tfoot tr")
var cssComment = regexp.MustCompile(`(?s)/\*.*?(?:\*/|$)`)
var numberPattern = regexp.MustCompile(`^-?\d*\.?\d+`)
var asciiWhitespace = regexp.MustCompile(`[\t \r\n]+`)
var backticks = regexp.MustCompile("`+")
var cssEscape = regexp.MustCompile(`\\(?:[0-9a-fA-F]{1,6}[ \t\r\n\f]?|[^\r\n\f])`)

// Author-hidden text leaves a marker so screening keeps the "this was hidden" cue that flattening otherwise removes.
// Non-rendered elements (style, script, head) and hidden tracking pixels leave nothing; they are not an author hiding prose.
const hiddenMarker = "[hidden text removed]"

var hiddenMarkers = regexp.MustCompile(`\[hidden text removed\](?:\s*\[hidden text removed\])+`)

func cssIdentifier(s string) string {
	return cssEscape.ReplaceAllStringFunc(s, func(escape string) string {
		code := strings.TrimSpace(escape[1:])
		if value, err := strconv.ParseInt(code, 16, 32); err == nil {
			if value == 0 || value > 0x10ffff || (value >= 0xd800 && value <= 0xdfff) {
				return "�"
			}
			return string(rune(value))
		}
		return escape[1:]
	})
}

func words(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}
func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

// Balance with a tokenizer first, like html.parser, and check raw nesting BEFORE
// invoking the HTML5 tree builder (misnested tables can otherwise be quadratic).
func balancedHTML(source string) (*html.Node, error) {
	root := &html.Node{Type: html.DocumentNode}
	stack := []*html.Node{root}
	z := html.NewTokenizer(strings.NewReader(source))
	for {
		kind := z.Next()
		if kind == html.ErrorToken {
			if z.Err() != io.EOF {
				return nil, errors.New("render ValueError")
			}
			break
		}
		tok := z.Token()
		switch kind {
		case html.StartTagToken, html.SelfClosingTagToken:
			n := &html.Node{Type: html.ElementNode, Data: tok.Data, Attr: tok.Attr}
			stack[len(stack)-1].AppendChild(n)
			if len(stack) > MaxDepth {
				return nil, errors.New("render ValueError")
			}
			if kind == html.StartTagToken && !voidTags[tok.Data] {
				stack = append(stack, n)
			}
		case html.EndTagToken:
			for i := len(stack) - 1; i > 0; i-- {
				if stack[i].Data == tok.Data {
					stack = stack[:i]
					break
				}
			}
		case html.TextToken:
			stack[len(stack)-1].AppendChild(&html.Node{Type: html.TextNode, Data: tok.Data})
		case html.CommentToken:
			stack[len(stack)-1].AppendChild(&html.Node{Type: html.CommentNode, Data: tok.Data})
		}
	}
	var b bytes.Buffer
	if err := html.Render(&b, root); err != nil {
		return nil, errors.New("render ValueError")
	}
	doc, err := html.Parse(&b)
	if err != nil {
		return nil, errors.New("render ValueError")
	}
	return doc, nil
}

// ponytail: inline style only, parsed leniently; malformed CSS is ignored, not refused.
// Stylesheets, colour contrast, geometry, clipping and !important precedence are left to screening;
// every published email payload used an inline hide on one element that these checks catch, none depended on the rest alone.
// Under-hiding is the safe direction: text we keep is text Jev screens.
// A semicolon inside url() or a quoted string splits the declaration; at worst that over-hides, which removes text from Jev and the agent alike.
func inlineStyle(n *html.Node) map[string]string {
	s := map[string]string{}
	for _, decl := range strings.Split(cssComment.ReplaceAllString(attr(n, "style"), ""), ";") {
		if k, v, ok := strings.Cut(decl, ":"); ok {
			v, _, _ = strings.Cut(v, "!")
			s[strings.ToLower(strings.TrimSpace(cssIdentifier(k)))] = strings.ToLower(strings.TrimSpace(cssIdentifier(v)))
		}
	}
	return s
}
func number(s string) (float64, bool) {
	m := numberPattern.FindString(s)
	if m == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(m, 64)
	return f, err == nil
}
func hidden(n *html.Node, s map[string]string) bool {
	opacity, ok := number(s["opacity"])
	return dropTags[n.Data] || hasAttr(n, "hidden") || s["display"] == "none" || s["mso-hide"] == "all" || s["visibility"] == "hidden" || s["visibility"] == "collapse" || (ok && opacity < .1)
}
func hasText(n *html.Node) bool {
	if n.Type == html.TextNode {
		return strings.TrimSpace(stripInvisible(n.Data)) != ""
	}
	if n.Type == html.ElementNode && dropTags[n.Data] {
		return false
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if hasText(ch) {
			return true
		}
	}
	return false
}
func normalizedTag(n *html.Node) string {
	if n == nil {
		return ""
	}
	if tableBlocks[n.Data] {
		return "div"
	}
	if n.Data == "td" || n.Data == "th" {
		return "span"
	}
	return n.Data
}
func block(n *html.Node) bool {
	return n != nil && n.Type == html.ElementNode && blocks[normalizedTag(n)]
}
func outsideBlock(n *html.Node) bool { return block(n) || (n != nil && n.Data == "pre") }
func chomp(s string) (string, string, string) {
	p, q := "", ""
	if strings.HasPrefix(s, " ") {
		p = " "
	}
	if strings.HasSuffix(s, " ") {
		q = " "
	}
	return p, strings.TrimSpace(s), q
}

type renderContext struct{ tiny, pre, noformat, inline, inLink, inListItem bool }

func htmlMarkdown(source string) (string, error) {
	doc, err := balancedHTML(source)
	if err != nil {
		return "", err
	}
	var convert func(*html.Node, renderContext) string
	convert = func(n *html.Node, ctx renderContext) string {
		if n.Type == html.TextNode {
			text := n.Data
			if ctx.tiny && hasText(n) {
				return " " + hiddenMarker + " "
			}
			if !ctx.pre {
				text = asciiWhitespace.ReplaceAllString(text, " ")
			}
			if outsideBlock(n.PrevSibling) || (block(n.Parent) && n.PrevSibling == nil) {
				text = strings.TrimLeft(text, " \t\r\n")
			}
			if outsideBlock(n.NextSibling) || (block(n.Parent) && n.NextSibling == nil) {
				text = strings.TrimRightFunc(text, unicode.IsSpace)
			}
			return cleanText(text)
		}
		if n.Type != html.ElementNode && n.Type != html.DocumentNode {
			return ""
		}
		s := inlineStyle(n)
		if hidden(n, s) {
			if !hasText(n) {
				return ""
			}
			if block(n) {
				return "\n\n" + hiddenMarker + "\n\n"
			}
			return " " + hiddenMarker + " "
		}
		tag := normalizedTag(n)
		child := ctx
		if font := s["font-size"]; font != "" {
			size, ok := number(font)
			child.tiny = ok && (size == 0 || (size < 2 && (strings.HasSuffix(font, "px") || strings.HasSuffix(font, "pt"))))
		}
		child.pre = ctx.pre || tag == "pre"
		child.noformat = ctx.noformat || tag == "pre" || tag == "code" || tag == "kbd" || tag == "samp"
		child.inline = ctx.inline || (len(tag) == 2 && tag[0] == 'h' && tag[1] >= '1' && tag[1] <= '6')
		child.inLink = ctx.inLink || tag == "a"
		child.inListItem = ctx.inListItem || tag == "li"
		var b strings.Builder
		pendingNewlines := 0
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			if ch.Type == html.TextNode && strings.TrimSpace(ch.Data) == "" && ((block(n) && (ch.PrevSibling == nil || ch.NextSibling == nil)) || outsideBlock(ch.PrevSibling) || outsideBlock(ch.NextSibling)) {
				continue
			}
			part := convert(ch, child)
			if part == "" {
				continue
			}
			if child.pre {
				b.WriteString(part)
				continue
			}
			leading := len(part) - len(strings.TrimLeft(part, "\n"))
			part = part[leading:]
			trailing := len(part) - len(strings.TrimRight(part, "\n"))
			if leading > 0 && pendingNewlines > 0 {
				leading = min(2, max(leading, pendingNewlines))
			} else {
				leading += pendingNewlines
			}
			b.WriteString(strings.Repeat("\n", leading))
			b.WriteString(part[:len(part)-trailing])
			pendingNewlines = trailing
		}
		b.WriteString(strings.Repeat("\n", pendingNewlines))
		text := b.String()
		if n.Data == "td" || n.Data == "th" {
			text += " "
		}
		p, trim, q := chomp(text)
		wrap := func(mark string) string {
			if ctx.noformat {
				return text
			}
			if trim == "" {
				return ""
			}
			return p + mark + trim + mark + q
		}
		switch tag {
		case "a":
			target := webURL(attr(n, "href"))
			label := strings.Join(strings.Fields(trim), " ")
			if target == "" || label == "" {
				return p + label + q
			}
			return p + "[" + label + "]\x00" + target + "\x00" + q
		case "img":
			label := "image"
			if alt := strings.Join(strings.Fields(cleanText(attr(n, "alt"))), " "); alt != "" {
				label += ": " + alt
			}
			if ctx.inLink {
				return label
			}
			target := webURL(attr(n, "src"))
			if target != "" {
				return "[" + label + "]\x00" + target + "\x00"
			}
			return "[" + label + "]"
		case "b", "strong":
			return wrap("**")
		case "i", "em":
			return wrap("*")
		case "del", "s":
			return wrap("~~")
		case "sub", "sup":
			return wrap("")
		case "code", "kbd", "samp":
			if ctx.noformat {
				return text
			}
			max := 0
			for _, run := range backticks.FindAllString(trim, -1) {
				if len(run) > max {
					max = len(run)
				}
			}
			if max > 0 {
				trim = " " + trim + " "
			}
			if trim == "" {
				return ""
			}
			marks := strings.Repeat("`", max+1)
			return p + marks + trim + marks + q
		case "h1", "h2", "h3", "h4", "h5", "h6":
			if ctx.inline {
				return text
			}
			return "\n\n" + strings.Repeat("#", int(tag[1]-'0')) + " " + asciiWhitespace.ReplaceAllString(trim, " ") + "\n\n"
		case "div", "article", "section", "p", "dl", "figcaption":
			if ctx.inline {
				return " " + trim + " "
			}
			if trim == "" {
				return ""
			}
			return "\n\n" + trim + "\n\n"
		case "blockquote":
			if ctx.inline {
				return " " + trim + " "
			}
			if trim == "" {
				return "\n"
			}
			lines := strings.Split(trim, "\n")
			for i, l := range lines {
				if l == "" {
					lines[i] = ">"
				} else {
					lines[i] = "> " + l
				}
			}
			return "\n" + strings.Join(lines, "\n") + "\n\n"
		case "br":
			if ctx.inline {
				return " "
			}
			return "  \n"
		case "hr":
			return "\n\n---\n\n"
		case "pre":
			if text == "" {
				return ""
			}
			return "\n\n```\n" + strings.Trim(text, " \n") + "\n```\n\n"
		case "q":
			return "\"" + text + "\""
		case "ul", "ol":
			if ctx.inListItem {
				return "\n" + strings.TrimRight(text, " \t\r\n")
			}
			return "\n\n" + text + "\n"
		case "li":
			if trim == "" {
				return "\n"
			}
			bullet := "- "
			if n.Parent != nil && n.Parent.Data == "ol" {
				start, err := strconv.Atoi(attr(n.Parent, "start"))
				if err != nil || start < 0 {
					start = 1
				}
				for prev := n.PrevSibling; prev != nil; prev = prev.PrevSibling {
					if prev.Data == "li" {
						start++
					}
				}
				bullet = fmt.Sprintf("%d. ", start)
			}
			lines := strings.Split(trim, "\n")
			for i := 1; i < len(lines); i++ {
				if lines[i] != "" {
					lines[i] = strings.Repeat(" ", len(bullet)) + lines[i]
				}
			}
			return bullet + strings.Join(lines, "\n") + "\n"
		case "dt":
			if ctx.inline {
				return " " + trim + " "
			}
			return "\n\n" + asciiWhitespace.ReplaceAllString(trim, " ") + "\n"
		case "dd":
			if ctx.inline {
				return " " + trim + " "
			}
			return ":   " + strings.ReplaceAll(trim, "\n", "\n    ") + "\n"
		case "caption":
			return trim + "\n\n"
		case "video":
			if ctx.inline {
				return text
			}
			src := cleanText(attr(n, "src"))
			poster := cleanText(attr(n, "poster"))
			if src != "" && poster != "" {
				return "[![" + text + "](" + poster + ")](" + src + ")"
			}
			if src != "" {
				return "[" + text + "](" + src + ")"
			}
			if poster != "" {
				return "![" + text + "](" + poster + ")"
			}
		}
		return text
	}
	return hiddenMarkers.ReplaceAllString(convert(doc, renderContext{}), hiddenMarker), nil
}
