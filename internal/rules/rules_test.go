package rules_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/rules"
)

// work and home are the account keys that the tests configure.
const (
	work = "work@example.test"
	home = "home@example.test"
)

// load returns the rules of account from a rules.toml whose tables use the short names work and home.
func load(t *testing.T, text, account string) (rules.Rules, error) {
	t.Helper()
	got, _, err := loadAll(t, text)
	return got[account], err
}

func loadAll(t *testing.T, text string) (map[string]rules.Rules, []string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rules.toml")
	text = strings.ReplaceAll(text, `\n`, "\n")
	text = strings.ReplaceAll(strings.ReplaceAll(text, "accounts.work", `accounts."`+work+`"`), "accounts.home", `accounts."`+home+`"`)
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return rules.Load(context.Background(), path, []string{work, home})
}
func TestInvalidRulesAnywhereAreRefusedWithoutValues(t *testing.T) {
	for _, text := range []string{
		"[accounts.work", "other = 'SECRET'", "accounts = 'SECRET'", "[accounts]\\n\"work@example.test\" = 1", "[accounts.work]\\nx = 1",
		"[accounts.work.Bad]\\nfolder = 'f'\\ninstructions = 'SECRET'",
		"[accounts.work.bad_name]\\nfolder = 'f'\\ninstructions = 'SECRET'",
		"[accounts.work.x]\\nfolder = 'f'", "[accounts.work.x]\\nfolder = 'f'\\ninstructions = ' '",
		"[accounts.work.x]\\nfolder = 'f'\\ninstructions = 3", "[accounts.work.x]\\nfolder = 'f'\\ncategory = 'C'\\ninstructions = 'SECRET'",
		"[accounts.work.x]\\ninstructions = 'SECRET'", "[accounts.work.x]\\nfolder = ''\\ninstructions = 'SECRET'",
		"[accounts.work.x]\\ncategory = 1\\ninstructions = 'SECRET'", "[accounts.work.x]\\nfolder = 'f'\\ninstructions = 'SECRET'\\ncolor = 'red'",
		"[accounts.work.x]\\ncategory = 'C'\\ninstructions = 'SECRET'\\nmin_age_hours = 1",
		"[accounts.home.x]\\nfolder = 'f'",
		"[accounts.work.stay]\\nfolder = 'f'\\ninstructions = 'SECRET'", "[accounts.work.stay]", "[accounts.work]\\nstay = 1",
		"[accounts.work.x]\\nfolder = 'f'\\ninstructions = 'SECRET'\\n[accounts.work.y]\\nfolder = 'f'\\ninstructions = 'SECRET'",
	} {
		for _, account := range []string{work, home} {
			if _, err := load(t, text, account); err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Errorf("unsafe or missing rejection for %q: %v", text, err)
			}
		}
	}
	for _, age := range []string{"true", "-1", "1.5", "'24'"} {
		if _, err := load(t, "[accounts.work.x]\\nfolder = 'f'\\ninstructions = 'x'\\nmin_age_hours = "+age, home); err == nil {
			t.Errorf("invalid age accepted: %s", age)
		}
	}
}

func TestInvalidUTF8IsRejectedEvenInComments(t *testing.T) {
	for _, text := range []string{
		"# invalid byte: \xff\n",
		"[accounts.work.x]\nfolder = 'f'\ninstructions = '\xff'\n",
	} {
		if _, err := load(t, text, work); err == nil {
			t.Fatal("invalid UTF-8 accepted")
		}
	}
}

func TestMissingAndUnconfiguredRulesAreEmpty(t *testing.T) {
	all, unknown, err := rules.Load(context.Background(), filepath.Join(t.TempDir(), "missing"), []string{work})
	if got := all[work]; err != nil || len(got.Rules) != 0 || got.Stay != rules.StayInstructions || len(unknown) != 0 {
		t.Fatalf("got=%v unknown=%v error=%v", got, unknown, err)
	}
	for _, text := range []string{"", "[accounts.home.bills]\\nfolder = 'f'\\ninstructions = 'x'"} {
		got, err := load(t, text, work)
		if err != nil || len(got.Rules) != 0 || got.Stay != rules.StayInstructions {
			t.Fatalf("got=%v error=%v", got, err)
		}
	}
}

func TestRulesKeepFileOrderExactTargetsAndStay(t *testing.T) {
	got, err := load(t, `[accounts.work.stay]
instructions = " Keep important mail. "
[accounts.work.finance]
folder = "f-fin"
instructions = " Receipts. "
[accounts.work.meeting-replies]
folder = "archive"
min_age_hours = 24
instructions = "Notifications."
[accounts.work.urgent]
category = " Urgent "
instructions = "At risk?"
[accounts.home.bills]
folder = "trash"
instructions = "Bills."
`, work)
	want := rules.Rules{Stay: "Keep important mail.", Rules: []rules.Rule{
		{Name: "finance", Kind: rules.Folder, Target: "f-fin", Instructions: "Receipts."},
		{Name: "meeting-replies", Kind: rules.Folder, Target: "archive", Instructions: "Notifications.", MinAgeHours: 24},
		{Name: "urgent", Kind: rules.Category, Target: " Urgent ", Instructions: "At risk?"},
	}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v error=%v", got, err)
	}
}

func TestRulesForAnUnknownEmailAreIgnoredAndReported(t *testing.T) {
	text := "[accounts.\"gone@example.test\".x]\nfolder = 'f'\n[accounts.\"WORK@example.test\".bills]\nfolder = 'f-bills'\ninstructions = 'Bills.'\n[accounts.\"old@example.test\".y]\ncategory = 1\n"
	got, unknown, err := loadAll(t, text)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(unknown, []string{"gone@example.test", "old@example.test"}) {
		t.Fatalf("unknown=%v", unknown)
	}
	if len(got) != 2 || len(got[work].Rules) != 1 || got[work].Rules[0].Target != "f-bills" || len(got[home].Rules) != 0 {
		t.Fatalf("rules=%+v", got)
	}
	if _, _, err := loadAll(t, "[accounts.\"work@example.test\".a]\nfolder = 'a'\ninstructions = 'x'\n[accounts.\"WORK@example.test\".b]\nfolder = 'b'\ninstructions = 'x'\n"); err == nil {
		t.Fatal("two tables for one account accepted")
	}
}
