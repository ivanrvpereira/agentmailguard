// Package rules reads organizer rules. It never writes user configuration.
package rules

import (
	"context"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

const Stay = "stay"
const StayInstructions = "Leave in Inbox: fits none of the other options."

type Kind string

const (
	Folder   Kind = "folder"
	Category Kind = "category"
)

type Rule struct {
	Name                 string
	Kind                 Kind
	Target, Instructions string
	MinAgeHours          int64
}
type Rules struct {
	Rules []Rule
	Stay  string
}

// Load reads the rules of every known account, keyed by account email. Tables are named by email, compared in
// lowercase. Each known account gets a value, with no rules when the file has none for it. Tables for an email that
// is not known are not validated or used: they are returned in unknown, in file order, so that left-over rules
// never stop the daemon. A missing file has no rules.
func Load(ctx context.Context, path string, known []string) (map[string]Rules, []string, error) {
	result := map[string]Rules{}
	for _, email := range known {
		result[strings.ToLower(email)] = Rules{Rules: []Rule{}, Stay: StayInstructions}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return result, []string{}, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%s: cannot read rules", path)
	}
	var document map[string]any
	metadata, err := toml.Decode(string(data), &document)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid TOML in %s", path)
	}
	for key := range document {
		if key != "accounts" {
			return nil, nil, fmt.Errorf("%s: unknown top-level key %q", path, key)
		}
	}
	accounts, ok := document["accounts"].(map[string]any)
	if document["accounts"] != nil && !ok {
		return nil, nil, fmt.Errorf("%s: 'accounts' must be a table of accounts", path)
	}
	parsed := map[string]map[string]Rule{}
	lower := map[string]bool{}
	for account, raw := range accounts {
		if _, known := result[strings.ToLower(account)]; !known {
			continue
		}
		where := fmt.Sprintf("%s: account %q", path, account)
		if lower[strings.ToLower(account)] {
			return nil, nil, fmt.Errorf("%s: another table names the same account", where)
		}
		lower[strings.ToLower(account)] = true
		tables, ok := raw.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("%s must be a table of rules", where)
		}
		targets := map[Kind]map[string]bool{Folder: {}, Category: {}}
		parsed[account] = map[string]Rule{}
		// Sort validation only; the returned rules retain file order below.
		for _, name := range slices.Sorted(maps.Keys(tables)) {
			rule, err := parseRule(where, name, tables[name])
			if err != nil {
				return nil, nil, err
			}
			parsed[account][name] = rule
			if name == Stay {
				continue
			}
			if targets[rule.Kind][rule.Target] {
				return nil, nil, fmt.Errorf("%s, rule %q: another rule has the same %s", where, name, rule.Kind)
			}
			targets[rule.Kind][rule.Target] = true
		}
	}
	unknown := []string{}
	seen := map[[2]string]bool{}
	for _, key := range metadata.Keys() {
		if len(key) < 2 || key[0] != "accounts" {
			continue
		}
		rules, known := parsed[key[1]]
		if !known {
			if !slices.Contains(unknown, key[1]) {
				unknown = append(unknown, key[1])
			}
			continue
		}
		if len(key) < 3 || seen[[2]string{key[1], key[2]}] {
			continue
		}
		seen[[2]string{key[1], key[2]}] = true
		account, rule := strings.ToLower(key[1]), rules[key[2]]
		r := result[account]
		if key[2] == Stay {
			r.Stay = rule.Instructions
		} else {
			r.Rules = append(r.Rules, rule)
		}
		result[account] = r
	}
	return result, unknown, ctx.Err()
}

var namePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

func parseRule(where, name string, raw any) (Rule, error) {
	here := fmt.Sprintf("%s, rule %q", where, name)
	fail := func(message string) (Rule, error) { return Rule{}, fmt.Errorf("%s: %s", here, message) }
	table, ok := raw.(map[string]any)
	if !ok {
		return fail("must be a table")
	}
	if name != Stay && !namePattern.MatchString(name) {
		return fail("a rule name uses only a-z, 0-9, and -")
	}
	instructions, ok := table["instructions"].(string)
	if !ok || strings.TrimSpace(instructions) == "" {
		return fail("missing or empty 'instructions'")
	}
	result := Rule{Name: name, Instructions: strings.TrimSpace(instructions)}
	if name == Stay {
		if len(table) != 1 {
			return fail("only 'instructions' is allowed")
		}
		return result, nil
	}
	_, folder := table["folder"]
	_, category := table["category"]
	if folder == category {
		return fail("needs exactly one of 'folder' or 'category'")
	}
	result.Kind = Folder
	if category {
		result.Kind = Category
	}
	for key := range table {
		if key != string(result.Kind) && key != "instructions" && !(key == "min_age_hours" && folder) {
			return fail(fmt.Sprintf("key not allowed on a %s rule: %s", result.Kind, key))
		}
	}
	result.Target, ok = table[string(result.Kind)].(string)
	if !ok || strings.TrimSpace(result.Target) == "" {
		return fail(fmt.Sprintf("%q must be a nonempty string", result.Kind))
	}
	if raw, exists := table["min_age_hours"]; exists {
		result.MinAgeHours, ok = raw.(int64)
		if !ok || result.MinAgeHours < 0 {
			return fail("'min_age_hours' must be a nonnegative integer")
		}
	}
	return result, nil
}
