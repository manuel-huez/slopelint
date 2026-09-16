package lint

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	similaritySignatureShortMinimumWords    = 4
	similaritySignatureStandardMinimumWords = 6
	similaritySignatureStandardMaximumWords = 24
	similaritySignatureLongMaximumWords     = 30
)

func (description *similarityDescription) normalize() {
	description.Purpose = normalizeSimilarityDescriptionText(description.Purpose)
	description.Subject = normalizeSimilarityDescriptionText(description.Subject)
	description.Scenario = normalizeSimilarityDescriptionText(description.Scenario)
	description.Contract = normalizeSimilarityDescriptionText(description.Contract)
	description.IntentSignature = normalizeSimilarityDescriptionText(description.IntentSignature)
	description.FlowSignature = normalizeSimilarityDescriptionText(description.FlowSignature)
	description.BoundarySignature = normalizeSimilarityDescriptionText(
		description.BoundarySignature,
	)
	description.ContractSignature = normalizeSimilarityDescriptionText(
		description.ContractSignature,
	)
	description.ScenarioSignature = normalizeSimilarityDescriptionText(
		description.ScenarioSignature,
	)
	description.OracleSignature = normalizeSimilarityDescriptionText(description.OracleSignature)
	description.Inputs = normalizeSimilarityDescriptionList(description.Inputs)
	description.Outputs = normalizeSimilarityDescriptionList(description.Outputs)
	description.Processing = normalizeSimilarityDescriptionList(description.Processing)
	description.Effects = normalizeSimilarityDescriptionList(description.Effects)
	description.Errors = normalizeSimilarityDescriptionList(description.Errors)
	description.Setup = normalizeSimilarityDescriptionList(description.Setup)
	description.Action = normalizeSimilarityDescriptionList(description.Action)
	description.Assertions = normalizeSimilarityDescriptionList(description.Assertions)
	description.Fixtures = normalizeSimilarityDescriptionList(description.Fixtures)
}

func normalizeSimilarityDescriptionText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func normalizeSimilarityDescriptionList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = normalizeSimilarityDescriptionText(value); value != "" {
			out = append(out, value)
		}
	}

	return out
}

func (description similarityDescription) validate() error {
	if description.Schema != similarityDescriptionPromptSchema ||
		description.Model != similarityDescriptionModel ||
		description.Effort != similarityDescriptionEffort {
		return errors.New("obsolete description policy")
	}

	switch description.Kind {
	case similarityDescriptionProduction:
		if !description.Detail {
			return description.validateProductionSignatures()
		}

		return description.validateProduction()
	case similarityDescriptionTest:
		if !description.Detail {
			return description.validateTestSignatures()
		}

		return description.validateTest()
	default:
		return fmt.Errorf("unknown description kind %q", description.Kind)
	}
}

func (description similarityDescription) validateProductionSignatures() error {
	// Prompt ranges keep signatures compact. Slightly wider hard bounds avoid
	// retrying a whole remote batch for one useful phrase off by a few words.
	if err := validateSimilarityDescriptionWords(
		"intent_signature",
		description.IntentSignature,
		similarityDescriptionSignatureMaxChars,
		similaritySignatureStandardMinimumWords,
		similaritySignatureStandardMaximumWords,
	); err != nil {
		return err
	}

	if err := validateSimilarityDescriptionWords(
		"flow_signature",
		description.FlowSignature,
		similarityDescriptionSignatureMaxChars,
		similaritySignatureStandardMinimumWords,
		similaritySignatureLongMaximumWords,
	); err != nil {
		return err
	}

	if description.BoundarySignature != "" {
		if err := validateSimilarityDescriptionWords(
			"boundary_signature",
			description.BoundarySignature,
			similarityDescriptionSignatureMaxChars,
			similaritySignatureShortMinimumWords,
			similaritySignatureStandardMaximumWords,
		); err != nil {
			return err
		}
	}

	if description.hasDetailFields() || description.hasTestSignatures() {
		return errors.New("production signature contains detail or test fields")
	}

	return nil
}

func (description similarityDescription) validateTestSignatures() error {
	if err := validateSimilarityDescriptionWords(
		"contract_signature",
		description.ContractSignature,
		similarityDescriptionSignatureMaxChars,
		similaritySignatureStandardMinimumWords,
		similaritySignatureStandardMaximumWords,
	); err != nil {
		return err
	}

	if err := validateSimilarityDescriptionWords(
		"scenario_signature",
		description.ScenarioSignature,
		similarityDescriptionSignatureMaxChars,
		similaritySignatureShortMinimumWords,
		similaritySignatureStandardMaximumWords,
	); err != nil {
		return err
	}

	if err := validateSimilarityDescriptionWords(
		"oracle_signature",
		description.OracleSignature,
		similarityDescriptionSignatureMaxChars,
		similaritySignatureStandardMinimumWords,
		similaritySignatureLongMaximumWords,
	); err != nil {
		return err
	}

	if description.hasDetailFields() || description.hasProductionSignatures() {
		return errors.New("test signature contains detail or production fields")
	}

	return nil
}

func (description similarityDescription) hasDetailFields() bool {
	return description.hasProductionDetailFields() || description.hasTestDetailFields()
}

func (description similarityDescription) hasProductionDetailFields() bool {
	return description.Purpose != "" || len(description.Inputs) > 0 ||
		len(description.Outputs) > 0 || len(description.Processing) > 0 ||
		len(description.Effects) > 0 || len(description.Errors) > 0
}

func (description similarityDescription) hasTestDetailFields() bool {
	return description.Subject != "" || description.Scenario != "" ||
		len(description.Setup) > 0 || len(description.Action) > 0 ||
		len(description.Assertions) > 0 || len(description.Fixtures) > 0 ||
		description.Contract != ""
}

func (description similarityDescription) hasProductionSignatures() bool {
	return description.IntentSignature != "" || description.FlowSignature != "" ||
		description.BoundarySignature != ""
}

func (description similarityDescription) hasTestSignatures() bool {
	return description.ContractSignature != "" || description.ScenarioSignature != "" ||
		description.OracleSignature != ""
}

func (description similarityDescription) validateProduction() error {
	if err := validateSimilarityDescriptionWords(
		"purpose",
		description.Purpose,
		similarityDescriptionSummaryMaxChars,
		similarityDescriptionMinimumWords,
		similarityDescriptionMaximumWords,
	); err != nil {
		return err
	}

	rules := []similarityDescriptionListRule{
		{name: "inputs", values: description.Inputs, maximum: similarityDescriptionMaximumInputs},
		{
			name:    "outputs",
			values:  description.Outputs,
			maximum: similarityDescriptionMaximumOutputs,
		},
		{
			name:    "processing",
			values:  description.Processing,
			minimum: 1,
			maximum: similarityDescriptionMaximumSteps,
		},
		{
			name:    "effects",
			values:  description.Effects,
			maximum: similarityDescriptionMaximumEffects,
		},
		{name: "errors", values: description.Errors, maximum: similarityDescriptionMaximumErrors},
	}
	if err := validateSimilarityDescriptionLists(rules); err != nil {
		return err
	}

	if description.hasTestDetailFields() || description.hasProductionSignatures() ||
		description.hasTestSignatures() {
		return errors.New("production description contains test fields")
	}

	return nil
}

func (description similarityDescription) validateTest() error {
	if err := validateSimilarityDescriptionText(
		"subject",
		description.Subject,
		similarityDescriptionSubjectMaxChars,
	); err != nil {
		return err
	}

	if err := validateSimilarityDescriptionText(
		"scenario",
		description.Scenario,
		similarityDescriptionItemMaxChars,
	); err != nil {
		return err
	}

	if err := validateSimilarityDescriptionWords(
		"contract",
		description.Contract,
		similarityDescriptionSummaryMaxChars,
		similarityDescriptionMinimumWords,
		similarityDescriptionMaximumWords,
	); err != nil {
		return err
	}

	rules := []similarityDescriptionListRule{
		{name: "setup", values: description.Setup, maximum: similarityDescriptionMaximumInputs},
		{
			name:    "action",
			values:  description.Action,
			minimum: 1,
			maximum: similarityDescriptionMaximumActions,
		},
		{
			name:    "assertions",
			values:  description.Assertions,
			minimum: 1,
			maximum: similarityDescriptionMaximumSteps,
		},
		{
			name:    "fixtures",
			values:  description.Fixtures,
			maximum: similarityDescriptionMaximumFixtures,
		},
	}
	if err := validateSimilarityDescriptionLists(rules); err != nil {
		return err
	}

	if description.hasProductionDetailFields() || description.hasProductionSignatures() ||
		description.hasTestSignatures() {
		return errors.New("test description contains production fields")
	}

	return nil
}

func validateSimilarityDescriptionLists(rules []similarityDescriptionListRule) error {
	for _, rule := range rules {
		if err := validateSimilarityDescriptionList(
			rule.name,
			rule.values,
			rule.minimum,
			rule.maximum,
		); err != nil {
			return err
		}
	}

	return nil
}

func validateSimilarityDescriptionWords(
	name string,
	value string,
	maximumChars int,
	minimumWords int,
	maximumWords int,
) error {
	if err := validateSimilarityDescriptionText(
		name,
		value,
		maximumChars,
	); err != nil {
		return err
	}

	words := len(strings.Fields(value))
	if words < minimumWords || words > maximumWords {
		return fmt.Errorf(
			"%s has %d words, want %d-%d",
			name,
			words,
			minimumWords,
			maximumWords,
		)
	}

	return nil
}

func validateSimilarityDescriptionText(name, value string, maximum int) error {
	length := utf8.RuneCountInString(value)
	if length == 0 || length > maximum {
		return fmt.Errorf("%s has %d characters, want 1-%d", name, length, maximum)
	}

	return nil
}

func validateSimilarityDescriptionList(
	name string,
	values []string,
	minimum int,
	maximum int,
) error {
	if len(values) < minimum || len(values) > maximum {
		return fmt.Errorf("%s has %d items, want %d-%d", name, len(values), minimum, maximum)
	}

	for index, value := range values {
		if err := validateSimilarityDescriptionText(
			fmt.Sprintf("%s[%d]", name, index),
			value,
			similarityDescriptionItemMaxChars,
		); err != nil {
			return err
		}
	}

	return nil
}

func (description similarityDescription) canonical() string {
	lines := []string{"KIND " + strings.ToUpper(description.Kind)}
	appendField := func(name, value string) {
		if value != "" {
			lines = append(lines, name+" "+value)
		}
	}
	appendList := func(name string, values []string) {
		for _, value := range values {
			lines = append(lines, name+" "+value)
		}
	}

	switch {
	case description.Kind == similarityDescriptionProduction && description.Detail:
		appendField("PURPOSE", description.Purpose)
		appendList("INPUT", description.Inputs)
		appendList("OUTPUT", description.Outputs)
		appendList("PROCESS", description.Processing)
		appendList("EFFECT", description.Effects)
		appendList("ERROR", description.Errors)
	case description.Detail:
		appendField("SUBJECT", description.Subject)
		appendField("SCENARIO", description.Scenario)
		appendList("SETUP", description.Setup)
		appendList("ACTION", description.Action)
		appendList("ASSERT", description.Assertions)
		appendList("FIXTURE", description.Fixtures)
		appendField("CONTRACT", description.Contract)
	case description.Kind == similarityDescriptionProduction:
		appendField("INTENT_SIGNATURE", description.IntentSignature)
		appendField("FLOW_SIGNATURE", description.FlowSignature)
		appendField("BOUNDARY_SIGNATURE", description.BoundarySignature)
	default:
		appendField("CONTRACT_SIGNATURE", description.ContractSignature)
		appendField("SCENARIO_SIGNATURE", description.ScenarioSignature)
		appendField("ORACLE_SIGNATURE", description.OracleSignature)
	}

	return strings.Join(lines, "\n")
}

func (description similarityDescription) embeddingText() string {
	if description.Kind == similarityDescriptionTest {
		return strings.Join([]string{
			"KIND TEST",
			"CONTRACT " + description.ContractSignature,
			"SCENARIO " + description.ScenarioSignature,
			"ORACLE " + description.OracleSignature,
		}, "\n")
	}

	lines := []string{
		"KIND PRODUCTION",
		"INTENT " + description.IntentSignature,
		"FLOW " + description.FlowSignature,
	}
	if description.BoundarySignature != "" {
		lines = append(lines, "BOUNDARY "+description.BoundarySignature)
	}

	return strings.Join(lines, "\n")
}
