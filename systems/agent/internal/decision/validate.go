package decision

import (
	"fmt"
	"math"
	"strconv"
)

// Validate checks the shared request shape before contacting a provider.
func (request Request) Validate() error {
	if !isState(request.State) {
		return fmt.Errorf("state must be a string, object, or array")
	}
	if len(request.Questions) < 1 || len(request.Questions) > 64 {
		return fmt.Errorf("requires 1-64 named questions")
	}
	for name, question := range request.Questions {
		if len(name) == 0 || len(name) > 100 {
			return fmt.Errorf(
				"question names require 1-100 ASCII letters, digits, underscores, dots, or hyphens",
			)
		}
		for _, r := range name {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-') {
				return fmt.Errorf("invalid question name")
			}
		}
		if err := question.validate(); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks named answers and normalizes tied choices to option order.
func (response Response) Validate(questions map[string]Question) error {
	answers := response.Answers
	if len(answers) != len(questions) {
		return fmt.Errorf("answer names do not match questions")
	}
	for name, q := range questions {
		a, ok := answers[name]
		if !ok || a.Type != q.Type {
			return fmt.Errorf("answer name or type does not match question")
		}
		switch q.Type {
		case "noul":
			if !probability(a.Noul) {
				return fmt.Errorf("noul must be a probability")
			}
		case "choice":
			keys := make([]string, len(q.Options))
			for i, option := range q.Options {
				keys[i] = option.Name
			}
			if err := validateDistribution(a, keys); err != nil {
				return err
			}
			best := keys[0]
			for _, key := range keys[1:] {
				if a.Probabilities[key] > a.Probabilities[best] {
					best = key
				}
			}
			p, exists := a.Probabilities[a.Choice]
			if !exists || p < a.Probabilities[best] {
				return fmt.Errorf("choice must name a highest-probability option")
			}
			a.Choice = best
			answers[name] = a
		case "score":
			keys := make([]string, len(q.Levels))
			for i := range q.Levels {
				keys[i] = strconv.Itoa(i)
			}
			if err := validateDistribution(a, keys); err != nil {
				return err
			}
			if a.Score == nil || *a.Score < 0 || *a.Score > float64(len(keys)-1) ||
				len(a.Legend) != len(keys) {
				return fmt.Errorf("invalid score or legend")
			}
			for i, key := range keys {
				if a.Legend[key] != q.Levels[i] {
					return fmt.Errorf("score legend does not match levels")
				}
			}
		}
	}
	return nil
}

func validateDistribution(answer Answer, keys []string) error {
	if !probability(answer.Confidence) || len(answer.Probabilities) != len(keys) {
		return fmt.Errorf("missing confidence or option probabilities")
	}
	sum := 0.0
	for _, key := range keys {
		p, ok := answer.Probabilities[key]
		if !ok || !probability(&p) {
			return fmt.Errorf("invalid option probabilities")
		}
		sum += p
	}
	if math.Abs(sum-1) > 0.01 {
		return fmt.Errorf("option probabilities do not sum to one")
	}
	return nil
}

func probability(p *float64) bool {
	return p != nil && !math.IsNaN(*p) && !math.IsInf(*p, 0) && *p >= 0 && *p <= 1
}
