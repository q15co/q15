// Package decision evaluates typed questions through the System One API.
package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Client evaluates typed questions independently of provider transport and model selection.
// Implementations return a validated answer for every named question.
type Client interface {
	Check(context.Context, Request) (Response, error)
}

// Option is one ordered Choice criterion. A nil description uses the name itself.
type Option struct {
	Name        string
	Description any
}

// NoulCriteria optionally describes the two sides of a yes/no judgment.
type NoulCriteria struct {
	True  any `json:"true,omitempty"`
	False any `json:"false,omitempty"`
}

// Question describes one of the three System One primitives.
type Question struct {
	Type         string
	Instructions any
	Options      []Option
	Levels       []string
	NoulCriteria *NoulCriteria
}

// Choice constructs a question whose option order also determines tie breaking.
func Choice(instructions string, options []Option) Question {
	return Question{Type: "choice", Instructions: instructions, Options: options}
}

// Noul constructs a question answered with the probability of true.
func Noul(instructions string) Question {
	return Question{Type: "noul", Instructions: instructions}
}

// Score constructs a question with levels ordered from lowest to highest.
func Score(instructions string, levels []string) Question {
	return Question{Type: "score", Instructions: instructions, Levels: levels}
}

// MarshalJSON emits the API's type-specific criteria shape.
func (q Question) MarshalJSON() ([]byte, error) {
	var criteria any
	switch q.Type {
	case "choice":
		criteria = orderedOptions(q.Options)
	case "score":
		criteria = q.Levels
	case "noul":
		if q.NoulCriteria != nil {
			criteria = q.NoulCriteria
		}
	}
	return json.Marshal(struct {
		Type         string `json:"type"`
		Instructions any    `json:"instructions"`
		Criteria     any    `json:"criteria,omitempty"`
	}{q.Type, q.Instructions, criteria})
}

type orderedOptions []Option

func (options orderedOptions) MarshalJSON() ([]byte, error) {
	// encoding/json sorts map keys, which would change the preferred tied option.
	var b bytes.Buffer
	b.WriteByte('{')
	for i, option := range options {
		if i > 0 {
			b.WriteByte(',')
		}
		name, _ := json.Marshal(option.Name)
		description, err := json.Marshal(option.Description)
		if err != nil {
			return nil, err
		}
		b.Write(name)
		b.WriteByte(':')
		b.Write(description)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func (q Question) validate() error {
	if !isState(q.Instructions) {
		return fmt.Errorf("instructions must be a string, object, or array")
	}
	switch q.Type {
	case "choice":
		if len(q.Options) < 2 || len(q.Options) > 26 || len(q.Levels) > 0 || q.NoulCriteria != nil {
			return fmt.Errorf("choice requires 2-26 options and no other criteria")
		}
		seen := make(map[string]bool, len(q.Options))
		for _, option := range q.Options {
			if strings.TrimSpace(option.Name) == "" || seen[option.Name] {
				return fmt.Errorf("choice option names must be nonempty and unique")
			}
			seen[option.Name] = true
		}
	case "score":
		if len(q.Levels) < 2 || len(q.Levels) > 26 || len(q.Options) > 0 || q.NoulCriteria != nil {
			return fmt.Errorf("score requires 2-26 ordered levels and no other criteria")
		}
		for _, level := range q.Levels {
			if strings.TrimSpace(level) == "" {
				return fmt.Errorf("score levels must be nonempty")
			}
		}
	case "noul":
		if len(q.Options) > 0 || len(q.Levels) > 0 {
			return fmt.Errorf("noul accepts only true/false criteria")
		}
	default:
		return fmt.Errorf("unsupported question type")
	}
	return nil
}

func isState(value any) bool {
	data, err := json.Marshal(value)
	return err == nil && len(data) > 0 && (data[0] == '"' || data[0] == '{' || data[0] == '[')
}

// Request asks 1-64 named questions about a shared state.
type Request struct {
	// Model may be empty for application routing; a provider adapter requires its native ID.
	Model     string              `json:"model"`
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
	Images    []string            `json:"images,omitempty"`
	KeepAlive any                 `json:"keep_alive,omitempty"`
}

// Answer holds exactly the fields for its Type; nil distinguishes absent numbers from zero.
type Answer struct {
	Type          string            `json:"type"`
	Choice        string            `json:"choice,omitempty"`
	Noul          *float64          `json:"noul,omitempty"`
	Score         *float64          `json:"score,omitempty"`
	Probabilities Probabilities     `json:"probabilities,omitempty"`
	Confidence    *float64          `json:"confidence,omitempty"`
	Legend        map[string]string `json:"legend,omitempty"`
}

// Probabilities maps every option to a numeric probability; null is not a probability.
type Probabilities map[string]float64

// UnmarshalJSON distinguishes a numeric zero from a malformed null option value.
func (p *Probabilities) UnmarshalJSON(data []byte) error {
	var values map[string]*float64
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	decoded := make(Probabilities, len(values))
	for name, value := range values {
		if value == nil {
			return fmt.Errorf("null option probability")
		}
		decoded[name] = *value
	}
	*p = decoded
	return nil
}

// Response maps answers by the caller's question names.
type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
}

// Usage reports the provider's token accounting.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}
