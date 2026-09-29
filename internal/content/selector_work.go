package content

import (
	"strings"

	"github.com/gorilla/css/scanner"
	"golang.org/x/net/html"
)

// This is a work estimate, not a CSS matcher: predicates are assumed to succeed and searches to exhaust every candidate.
// For A B at node n, the bound is cost(B,n) + sum(1+cost(A,p)) over n's ancestors; A~B uses preceding siblings.
// Child/adjacent combinators use only their actual candidate; :not adds its argument's cost, while :has/:haschild sum it over descendants/children.
// These recurrences include repeated visits in Cascadia's backtracking.
// Prefix sums on the actual tree compute each recurrence in O(nodes), not exponentially.
// ponytail: cap estimated matching plus preflight vector work at 20M units per body.
// Refuse on exhaustion; a tighter predicate-aware estimator can replace this conservative bound if legitimate mail reaches the ceiling.
const maxSelectorWork = 20_000_000

type selectorWork struct {
	nodes            []*html.Node // preorder, including non-elements Cascadia can visit
	parent, previous []int
	local            []int // local predicate/attribute/text scan, computed once
	remaining        int
	tokens           []*scanner.Token
	at               int
}

func selectorsWithinBudget(doc *html.Node, sources []string) bool {
	if len(sources) == 0 {
		return true
	}
	w := selectorWork{remaining: maxSelectorWork}
	var visit func(*html.Node, int, int) int
	visit = func(n *html.Node, parent, previous int) int {
		i := len(w.nodes)
		w.nodes = append(w.nodes, n)
		w.parent = append(w.parent, parent)
		w.previous = append(w.previous, previous)
		local := workSum(1, len(n.Data))
		for _, a := range n.Attr {
			local = workSum(local, workSum(1, workSum(len(a.Key), len(a.Val))))
		}
		w.local = append(w.local, local)
		prev := -1
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			prev = visit(child, i, prev)
		}
		return i
	}
	visit(doc, -1, -1)
	for _, source := range sources {
		w.tokens, w.at = nil, 0
		s := scanner.New(source)
		for token := s.Next(); token.Type != scanner.TokenEOF; token = s.Next() {
			w.remaining--
			if token.Type == scanner.TokenError || w.remaining < 0 {
				return false
			}
			w.tokens = append(w.tokens, token)
		}
		cost := w.group()
		if cost == nil || w.at != len(w.tokens) {
			return false
		}
		for i, n := range w.nodes {
			if n.Type == html.ElementNode {
				w.remaining -= cost[i]
				if w.remaining < 0 {
					return false
				}
			}
		}
	}
	return true
}

// Saturation prevents hostile expressions from overflowing into a small bound.
func workSum(a, b int) int { return min(maxSelectorWork+1, a+b) }

func (w *selectorWork) vector() []int {
	w.remaining -= len(w.nodes)
	if w.remaining < 0 {
		return nil
	}
	return make([]int, len(w.nodes))
}
func (w *selectorWork) add(a, b []int) []int {
	if a == nil || b == nil {
		return nil
	}
	w.remaining -= len(w.nodes)
	if w.remaining < 0 {
		return nil
	}
	for i := range a {
		a[i] = workSum(a[i], b[i])
	}
	return a
}

// Transfers include one unit per traversed edge.
// All operations are linear in the DOM size, including sums over all ancestors, siblings or descendants.
func (w *selectorWork) transfer(cost []int, direction byte) []int {
	if cost == nil {
		return nil
	}
	out := w.vector()
	if out == nil {
		return nil
	}
	if direction == 'd' || direction == 'c' { // descendants / children
		for i := len(w.nodes) - 1; i >= 0; i-- {
			if p := w.parent[i]; p >= 0 {
				value := workSum(1, cost[i])
				if direction == 'd' {
					value = workSum(value, out[i])
				}
				out[p] = workSum(out[p], value)
			}
		}
		return out
	}
	for i := range w.nodes {
		switch direction {
		case ' ', '>':
			if p := w.parent[i]; p >= 0 {
				out[i] = workSum(1, cost[p])
				if direction == ' ' {
					out[i] = workSum(out[i], out[p])
				}
			}
		case '~', '+':
			if p := w.previous[i]; p >= 0 {
				out[i] = workSum(1, cost[p])
				if direction == '~' {
					out[i] = workSum(out[i], out[p])
				} else if w.nodes[p].Type == html.TextNode || w.nodes[p].Type == html.CommentNode {
					out[i] = workSum(1, out[p])
				}
			}
		}
	}
	return out
}

func (w *selectorWork) primitive(name string) []int {
	cost := w.vector()
	if cost == nil {
		return nil
	}
	copy(cost, w.local)
	switch name {
	case "nth-child", "nth-last-child", "nth-of-type", "nth-last-of-type", "first-child", "last-child", "first-of-type", "last-of-type", "only-child", "only-of-type":
		return w.add(cost, w.transfer(w.transfer(cost, 'c'), '>'))
	case "empty", "containsown", "matchesown":
		return w.add(cost, w.transfer(cost, 'c'))
	case "contains", "matches":
		return w.add(cost, w.transfer(cost, 'd'))
	case "lang":
		return w.add(cost, w.transfer(cost, ' '))
	case "enabled", "disabled":
		cost = w.add(cost, w.transfer(cost, '~'))
		return w.add(cost, w.transfer(cost, ' '))
	default:
		return cost
	}
}

func (w *selectorWork) value() string {
	if w.at == len(w.tokens) {
		return ""
	}
	return w.tokens[w.at].Value
}
func (w *selectorWork) spaces() {
	for w.at < len(w.tokens) && w.tokens[w.at].Type == scanner.TokenS {
		w.at++
	}
}
func (w *selectorWork) group() []int {
	var total []int
	for {
		w.spaces()
		cost := w.compound()
		if cost == nil {
			return nil
		}
		for {
			w.spaces()
			next := w.value()
			if next == "" || next == "," || next == ")" {
				break
			}
			direction := byte(' ')
			if next == ">" || next == "+" || next == "~" {
				direction = next[0]
				w.at++
				w.spaces()
			}
			right := w.compound()
			cost = w.add(right, w.transfer(cost, direction))
			if cost == nil {
				return nil
			}
		}
		if total == nil {
			total = cost
		} else {
			total = w.add(total, cost)
		}
		if total == nil || w.value() != "," {
			return total
		}
		w.at++
	}
}
func (w *selectorWork) compound() []int {
	cost := w.vector()
	start := w.at
	for w.at < len(w.tokens) {
		token := w.tokens[w.at]
		if token.Type == scanner.TokenS || strings.Contains(",)>+~", token.Value) {
			break
		}
		w.at++
		var part []int
		if token.Value == "[" {
			for w.at < len(w.tokens) && w.value() != "]" {
				w.at++ // Attribute strings and escaped identifiers are opaque tokens.
			}
			if w.at == len(w.tokens) {
				return nil
			}
			w.at++
			part = w.primitive("")
		} else if token.Type == scanner.TokenFunction {
			name := strings.ToLower(cssIdentifier(strings.TrimSuffix(token.Value, "(")))
			if name == "not" || name == "has" || name == "haschild" {
				part = w.group()
				if w.value() != ")" {
					return nil
				}
				w.at++
				if name == "has" {
					part = w.transfer(part, 'd')
				} else if name == "haschild" {
					part = w.transfer(part, 'c')
				}
			} else {
				depth := 1
				for w.at < len(w.tokens) && depth > 0 {
					t := w.tokens[w.at]
					if t.Type == scanner.TokenFunction || t.Value == "(" {
						depth++
					} else if t.Value == ")" {
						depth--
					}
					w.at++
				}
				if depth != 0 {
					return nil
				}
				part = w.primitive(name)
			}
		} else {
			name := ""
			if w.at >= 2 && w.tokens[w.at-2].Value == ":" {
				name = strings.ToLower(cssIdentifier(token.Value))
			}
			part = w.primitive(name)
		}
		cost = w.add(cost, part)
		if cost == nil {
			return nil
		}
	}
	if w.at == start {
		return nil
	}
	return cost
}
