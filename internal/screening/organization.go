package screening

import (
	"sort"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/rules"
)

const FolderInstructions = "Where does this email belong? Choose the option that fits it best."

// OrganizationQuestions builds the folder/stay Choice and independent category Nouls.
// Rules must have passed rules.Load validation. No instructions appear in returned answers.
func OrganizationQuestions(r rules.Rules) *model.OrganizationQuestions {
	result := &model.OrganizationQuestions{Categories: map[string]string{}}
	folders := map[string]string{}
	for _, rule := range r.Rules {
		switch rule.Kind {
		case rules.Folder:
			folders[rule.Name] = rule.Instructions
		case rules.Category:
			result.Categories[rule.Name] = rule.Instructions
		}
	}
	if len(folders) > 0 {
		stay := r.Stay
		if stay == "" {
			stay = rules.StayInstructions
		}
		folders[rules.Stay] = stay
		result.Folder = &model.ChoiceQuestion{Instructions: FolderInstructions, Criteria: folders}
	}
	if result.Folder == nil && len(result.Categories) == 0 {
		return nil
	}
	return result
}
func validateOrganization(answers map[string]answer, asked *model.OrganizationQuestions) (*model.Organization, string) {
	result := &model.Organization{Categories: map[string]float64{}}
	if asked.Folder != nil {
		a, ok := answers["folder"]
		options := asked.Folder.Criteria
		if !ok || a.Type != "choice" || !unit(a.Confidence) || len(a.Probabilities) != len(options) {
			return nil, "invalid answer folder"
		}
		if _, ok := options[a.Choice]; !ok {
			return nil, "invalid answer folder"
		}
		for name := range options {
			if _, ok := a.Probabilities[name]; !ok {
				return nil, "invalid answer folder"
			}
		}
		result.Folder = &model.FolderAnswer{Choice: a.Choice, Confidence: a.Confidence, Probabilities: a.Probabilities}
		if result.Validate() != nil {
			return nil, "invalid answer folder"
		}
	}
	names := make([]string, 0, len(asked.Categories))
	for name := range asked.Categories {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		a, ok := answers["category:"+name]
		if !model.ValidRuleName(name) || !ok || a.Type != "noul" || !unit(a.Noul) {
			return nil, "invalid answer category"
		}
		result.Categories[name] = a.Noul
	}
	return result, ""
}
