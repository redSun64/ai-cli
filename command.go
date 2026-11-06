package aicli

import (
	"context"
	"math"
	"reflect"
	"regexp"
)

// Risk declares business effects independently of HTTP verbs.
// Only READ/NONE and WRITE/REQUIRED are valid.
type Risk struct {
	Effect       string `json:"effect"`
	Confirmation string `json:"confirmation"`
}

// ReadRisk returns the risk of a read-only business command.
func ReadRisk() Risk { return Risk{"READ", "NONE"} }

// WriteRisk returns the risk of a business command requiring explicit approval.
func WriteRisk() Risk { return Risk{"WRITE", "REQUIRED"} }

// OptionType describes a supported command argument type.
type OptionType string

const (
	String  OptionType = "string"
	Boolean OptionType = "boolean"
	Number  OptionType = "number"
)

// Option defines an argument using its CLI spelling, e.g. --account-id.
// Default must match Type; numeric defaults and inputs accept Go numeric types.
type Option struct {
	Name        string
	Type        OptionType
	Required    bool
	Default     any
	Description string
	Sensitive   bool
}

// AgentMetadata describes when an Agent should select this command.
type AgentMetadata struct {
	Summary   string   `json:"summary"`
	UseWhen   []string `json:"useWhen"`
	AvoidWhen []string `json:"avoidWhen"`
	Examples  []string `json:"examples"`
}

// Arguments contains validated values indexed by CLI option name.
// Values of type Number are normalized to float64.
type Arguments map[string]any

// String returns an optional string argument, or an empty string when absent.
func (a Arguments) String(name string) string { v, _ := a[name].(string); return v }

// Bool returns an optional boolean argument, or false when absent.
func (a Arguments) Bool(name string) bool { v, _ := a[name].(bool); return v }

// Number returns an optional numeric argument, or zero when absent.
func (a Arguments) Number(name string) float64 { v, _ := a[name].(float64); return v }

// Invocation supplies capabilities scoped to one validated Runtime execution.
// Use Context for cancellation and HTTP for authenticated business requests.
type Invocation struct {
	Context context.Context
	Args    Arguments
	HTTP    *Client
}

// Handler implements a business workflow. Returning an Error preserves its code
// and recovery details. Side effects are not automatically rolled back.
type Handler func(Invocation) (any, error)

// Command is either a group with Children, or a leaf with Risk and Run.
// Groups cannot define options, risk, Agent metadata or a handler.
type Command struct {
	Name        string
	Description string
	Children    []Command
	Options     []Option
	Risk        Risk
	Agent       *AgentMetadata
	Run         Handler
}

var optionName = regexp.MustCompile(`^--[a-zA-Z0-9][a-zA-Z0-9_-]*$`)
var commandName = regexp.MustCompile(`^[a-zA-Z0-9+][a-zA-Z0-9_+.-]*$`)
var sensitiveName = regexp.MustCompile(`(?i)password|passwd|token|secret|credential|api[-_]?key|private[-_]?key|access[-_]?key`)

func isSensitive(o Option) bool { return o.Sensitive || sensitiveName.MatchString(o.Name) }

func normalize(value any, kind OptionType) (any, bool) {
	if value == nil {
		return nil, true
	}
	switch kind {
	case String:
		v, ok := value.(string)
		return v, ok
	case Boolean:
		v, ok := value.(bool)
		return v, ok
	case Number:
		r := reflect.ValueOf(value)
		var v float64
		switch r.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			v = float64(r.Int())
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			v = float64(r.Uint())
		case reflect.Float32, reflect.Float64:
			v = r.Float()
		default:
			return nil, false
		}
		return v, !math.IsNaN(v) && !math.IsInf(v, 0)
	}
	return nil, false
}
