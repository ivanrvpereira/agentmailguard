// Package rules reads organizer rules. It never writes user configuration.
package rules

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
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

// Load validates every account, including accounts other than the selected one.
// knownAccounts comes from config.AccountNames, keeping credential data out of this parser.
func Load(ctx context.Context, path, account string, knownAccounts []string) (Rules, error) {
	empty := Rules{Rules: []Rule{}, Stay: StayInstructions}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return empty, nil
	}
	if err != nil {
		return empty, fmt.Errorf("%s: cannot read rules", path)
	}
	var document map[string]any
	metadata, err := toml.Decode(string(data), &document)
	if err != nil {
		return empty, fmt.Errorf("invalid TOML in %s", path)
	}
	for key := range document {
		if key != "accounts" {
			return empty, fmt.Errorf("%s: unknown top-level key %q", path, key)
		}
	}
	accounts, ok := document["accounts"].(map[string]any)
	if document["accounts"] != nil && !ok {
		return empty, fmt.Errorf("%s: 'accounts' must be a table of accounts", path)
	}
	selected := map[string]Rule{}
	for accountName, raw := range accounts {
		where := fmt.Sprintf("%s: account %q", path, accountName)
		if !slices.Contains(knownAccounts, accountName) {
			return empty, fmt.Errorf("%s: account not in config.toml", where)
		}
		tables, ok := raw.(map[string]any)
		if !ok {
			return empty, fmt.Errorf("%s must be a table of rules", where)
		}
		targets := map[Kind]map[string]bool{Folder: {}, Category: {}}
		// Sort validation only; the returned rules retain file order below.
		names := make([]string, 0, len(tables))
		for name := range tables {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			rule, err := parseRule(where, name, tables[name])
			if err != nil {
				return empty, err
			}
			if accountName == account {
				selected[name] = rule
			}
			if name == Stay {
				continue
			}
			if targets[rule.Kind][rule.Target] {
				return empty, fmt.Errorf("%s, rule %q: another rule has the same %s", where, name, rule.Kind)
			}
			targets[rule.Kind][rule.Target] = true
		}
	}
	result := empty
	seen := map[string]bool{}
	for _, key := range metadata.Keys() {
		if len(key) < 3 || key[0] != "accounts" || key[1] != account || seen[key[2]] {
			continue
		}
		name := key[2]
		seen[name] = true
		rule := selected[name]
		if name == Stay {
			result.Stay = rule.Instructions
			continue
		}
		result.Rules = append(result.Rules, rule)
	}
	return result, ctx.Err()
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
