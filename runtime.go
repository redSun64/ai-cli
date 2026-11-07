package aicli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// Config assembles one business CLI. Construction does not read credentials,
// access the network or execute business handlers.
type Config struct {
	Name        string
	Description string
	Version     string
	Commands    []Command
	HTTP        *HTTPConfig
	Auth        *AuthManager
	Skill       *SkillBundle
	// Timeout bounds each execution cooperatively through Invocation.Context.
	// Handlers must honor context cancellation; default is 30 seconds.
	Timeout time.Duration
}

// Runtime is immutable after construction and supports concurrent executions.
// Handlers and custom auth/store implementations must themselves be concurrency-safe.
type Runtime struct {
	name, description, version string
	leaves                     map[string]Command
	groups                     map[string]string
	http                       *httpSettings
	auth                       *AuthManager
	skill                      *SkillBundle
	timeout                    time.Duration
}

// New validates and snapshots the command tree and connection settings.
func New(config Config) (*Runtime, error) {
	if !commandName.MatchString(config.Name) {
		return nil, failure("CONFIG_INVALID", "A valid CLI name is required.")
	}
	r := &Runtime{name: config.Name, description: config.Description, version: config.Version,
		leaves: map[string]Command{}, groups: map[string]string{}, auth: config.Auth, timeout: config.Timeout}
	if r.timeout < 0 {
		return nil, failure("CONFIG_INVALID", "Timeout must be positive.")
	}
	if r.timeout == 0 {
		r.timeout = 30 * time.Second
	}
	if config.Skill != nil {
		bundle, err := config.Skill.snapshot()
		if err != nil {
			return nil, err
		}
		r.skill = bundle
	}
	if config.HTTP != nil {
		settings, err := newHTTPSettings(*config.HTTP, config.Auth)
		if err != nil {
			return nil, err
		}
		r.http = settings
	}
	var collect func([]Command, []string, int) error
	collect = func(nodes []Command, prefix []string, depth int) error {
		if depth > 64 {
			return failure("CONFIG_INVALID", "Command tree is cyclic or too deep.")
		}
		for _, node := range nodes {
			if !commandName.MatchString(node.Name) {
				return failure("CONFIG_INVALID", "Invalid command name.")
			}
			if len(prefix) == 0 && reserved(node.Name) {
				return failure("CONFIG_INVALID", "Reserved command name: "+node.Name)
			}
			path := append(append([]string{}, prefix...), node.Name)
			key := strings.Join(path, " ")
			if node.definitionError != nil {
				return failure("CONFIG_INVALID", "Invalid definition for "+key+": "+node.definitionError.Error())
			}
			if node.input != nil && !node.input.matches(node.Options) {
				return failure("CONFIG_INVALID", "Typed options are derived from input fields; do not replace Options: "+key)
			}
			if _, ok := r.groups[key]; ok {
				return failure("CONFIG_INVALID", "Duplicate command: "+key)
			}
			if _, ok := r.leaves[key]; ok {
				return failure("CONFIG_INVALID", "Duplicate command: "+key)
			}
			if node.Children != nil {
				if node.Run != nil || len(node.Options) > 0 || node.Risk != (Risk{}) || node.Agent != nil {
					return failure("CONFIG_INVALID", "Groups cannot define execution or risk: "+key)
				}
				r.groups[key] = node.Description
				if err := collect(node.Children, path, depth+1); err != nil {
					return err
				}
				continue
			}
			if node.Run == nil || (node.Risk != ReadRisk() && node.Risk != WriteRisk()) {
				return failure("CONFIG_INVALID", "Leaf requires handler and valid risk: "+key)
			}
			node.Options = append([]Option{}, node.Options...)
			names := map[string]bool{}
			for i, option := range node.Options {
				if !optionName.MatchString(option.Name) || names[option.Name] || globalOption(option.Name) || (option.Type != String && option.Type != Boolean && option.Type != Number) {
					return failure("CONFIG_INVALID", "Invalid or duplicate option for "+key)
				}
				names[option.Name] = true
				value, ok := normalize(option.Default, option.Type)
				if !ok {
					return failure("CONFIG_INVALID", "Invalid option default: "+option.Name)
				}
				node.Options[i].Default = value
			}
			node.Agent = cloneAgent(node.Agent)
			r.leaves[key] = node
		}
		return nil
	}
	if err := collect(config.Commands, nil, 0); err != nil {
		return nil, err
	}
	return r, nil
}

func cloneAgent(a *AgentMetadata) *AgentMetadata {
	if a == nil {
		return nil
	}
	copy := *a
	copy.UseWhen = append([]string{}, a.UseWhen...)
	copy.AvoidWhen = append([]string{}, a.AvoidWhen...)
	copy.Examples = append([]string{}, a.Examples...)
	return &copy
}
func reserved(name string) bool {
	return name == "schema" || name == "auth" || name == "skills" || name == "help"
}
func globalOption(name string) bool {
	return name == "--yes" || name == "--dry-run" || name == "--format" || name == "--help" || name == "--version"
}

// ExecuteOptions controls preview and approval. Confirmed records an approval
// supplied by the caller; the SDK does not authenticate the approver.
type ExecuteOptions struct {
	DryRun    bool
	Confirmed bool
}

// Plan is a side-effect-free preview of a validated command.
type Plan struct {
	Command   string    `json:"command"`
	DryRun    bool      `json:"dryRun"`
	Arguments Arguments `json:"arguments"`
	Risk      Risk      `json:"risk"`
}

// Execute invokes the same command used by the CLI. DryRun validates arguments
// but never invokes the handler, auth provider or HTTP transport.
func (r *Runtime) Execute(ctx context.Context, path []string, input Arguments, options ExecuteOptions) (data any, err error) {
	if !validPath(path) {
		return nil, failure("UNKNOWN_COMMAND", "Unknown command path.")
	}
	command, ok := r.leaves[strings.Join(path, " ")]
	if !ok {
		return nil, failure("UNKNOWN_COMMAND", "Unknown command path.")
	}
	args := Arguments{}
	secrets := []string{}
	for key := range input {
		found := false
		for _, option := range command.Options {
			if option.Name == key {
				found = true
				break
			}
		}
		if !found {
			return nil, failure("INVALID_ARGUMENT", "Unknown option.")
		}
	}
	for _, option := range command.Options {
		value, exists := input[option.Name]
		if !exists || value == nil {
			value = option.Default
		}
		if option.Required && value == nil {
			return nil, failure("INVALID_ARGUMENT", "Missing required option: "+option.Name)
		}
		value, valid := normalize(value, option.Type)
		if !valid {
			return nil, failure("INVALID_ARGUMENT", "Invalid value for "+option.Name)
		}
		args[option.Name] = value
		if isSensitive(option) && value != nil {
			secrets = append(secrets, fmt.Sprint(value))
		}
	}
	if command.input != nil {
		if _, err := command.input.bind(args); err != nil {
			return nil, err
		}
	}
	if options.DryRun {
		for _, option := range command.Options {
			if isSensitive(option) && args[option.Name] != nil {
				args[option.Name] = "[REDACTED]"
			}
		}
		return Plan{strings.Join(path, " "), true, args, command.Risk}, nil
	}
	if command.Risk == WriteRisk() && !options.Confirmed {
		return nil, failure("WRITE_APPROVAL_REQUIRED", "This write command requires explicit approval before execution.")
	}
	execCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	if execCtx.Err() != nil {
		return nil, failure("EXECUTION_CANCELLED", "Execution context is cancelled.")
	}
	lease := &execution{ctx: execCtx, write: command.Risk == WriteRisk()}
	lease.active.Store(true)
	defer lease.active.Store(false)
	data, err = command.Run(Invocation{execCtx, args, &Client{settings: r.http, execution: lease}})
	if err != nil {
		return nil, sanitizeError(err, secrets)
	}
	if data == nil {
		data = map[string]any{"command": strings.Join(path, " "), "status": "completed"}
	}
	return data, nil
}

type execution struct {
	ctx    context.Context
	write  bool
	active atomic.Bool
}

// SchemaOption is the language-neutral discovery representation of an option.
type SchemaOption struct {
	Name         string  `json:"name"`
	Required     bool    `json:"required"`
	Type         string  `json:"type"`
	Description  string  `json:"description"`
	DefaultValue *string `json:"defaultValue,omitempty"`
	Sensitive    bool    `json:"sensitive,omitempty"`
	Integer      bool    `json:"integer,omitempty"`
}

// CommandSummary describes one immediate child without expanding descendants.
type CommandSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// CommandSchema is the progressive discovery contract consumed by Agents.
type CommandSchema struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Subcommands []CommandSummary `json:"subcommands,omitempty"`
	Options     []SchemaOption   `json:"options,omitempty"`
	Agent       *AgentMetadata   `json:"agent,omitempty"`
	Risk        *Risk            `json:"risk,omitempty"`
}

// Schema discovers a root, group or leaf without network or credential access.
func (r *Runtime) Schema(path ...string) (CommandSchema, error) {
	if len(path) > 0 && !validPath(path) {
		return CommandSchema{}, failure("UNKNOWN_COMMAND", "Unknown command path.")
	}
	key := strings.Join(path, " ")
	schema := CommandSchema{Name: r.name, Description: r.description}
	if len(path) > 0 {
		schema.Name = path[len(path)-1]
	}
	if node, ok := r.leaves[key]; ok {
		schema.Description, schema.Agent = node.Description, cloneAgent(node.Agent)
		risk := node.Risk
		schema.Risk = &risk
		for _, o := range node.Options {
			types := map[OptionType]string{String: "String", Number: "Double", Boolean: "boolean"}
			item := SchemaOption{Name: o.Name, Required: o.Required, Type: types[o.Type], Description: o.Description, Sensitive: isSensitive(o)}
			if node.input != nil {
				item.Integer = node.input.integer(o.Name)
			}
			if o.Default != nil && !item.Sensitive {
				v := fmt.Sprint(o.Default)
				item.DefaultValue = &v
			}
			schema.Options = append(schema.Options, item)
		}
		schema.Options = append(schema.Options, runtimeOptions()...)
		return schema, nil
	}
	if len(path) > 0 {
		description, ok := r.groups[key]
		if !ok {
			return r.builtinSchema(path)
		}
		schema.Description = description
	}
	prefix := key
	if prefix != "" {
		prefix += " "
	}
	children := map[string]string{}
	for group, description := range r.groups {
		if strings.HasPrefix(group, prefix) && !strings.Contains(strings.TrimPrefix(group, prefix), " ") {
			children[strings.TrimPrefix(group, prefix)] = description
		}
	}
	for leaf, node := range r.leaves {
		if strings.HasPrefix(leaf, prefix) && !strings.Contains(strings.TrimPrefix(leaf, prefix), " ") {
			children[strings.TrimPrefix(leaf, prefix)] = node.Description
		}
	}
	if len(path) == 0 {
		if r.auth != nil {
			children["auth"] = "Manage domain credentials."
		}
		if r.skill != nil {
			children["skills"] = "Install business Agent Skill."
		}
	}
	for name, description := range children {
		schema.Subcommands = append(schema.Subcommands, CommandSummary{name, description})
	}
	sort.Slice(schema.Subcommands, func(i, j int) bool { return schema.Subcommands[i].Name < schema.Subcommands[j].Name })
	return schema, nil
}

func validPath(path []string) bool {
	if len(path) == 0 {
		return false
	}
	for _, part := range path {
		if !commandName.MatchString(part) {
			return false
		}
	}
	return true
}

func runtimeOptions() []SchemaOption {
	text := "TEXT"
	return []SchemaOption{
		{Name: "--format", Type: "OutputFormat", Description: "Output format: TEXT, JSON", DefaultValue: &text},
		{Name: "--dry-run", Type: "boolean", Description: "Show the planned business action without executing the handler."},
		{Name: "-y", Type: "boolean", Description: "Alias for --yes."},
		{Name: "--yes", Type: "boolean", Description: "Confirm a REQUIRED write command."},
	}
}
