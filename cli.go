package aicli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// CLIOptions supplies optional platform-specific secret input. ReadSecret must
// hide terminal input and honor context cancellation. When nil, auth login returns
// AUTH_INTERACTIVE_REQUIRED; automation should inject credentials via environment.
type CLIOptions struct {
	ReadSecret func(context.Context) (string, error)
}

// RunCLI executes Cobra with argv (excluding executable) and returns an exit code:
// 0 success, 2 failure, 3 missing credentials. It never calls os.Exit. Each call
// creates a fresh Cobra tree so repeated and concurrent calls do not share flags.
func (r *Runtime) RunCLI(ctx context.Context, argv []string, out, errOut io.Writer, options CLIOptions) int {
	root := r.CobraCommand(options)
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs(argv)
	var missingAuth bool
	if r.auth != nil {
		status, _, _ := root.Find([]string{"auth", "status"})
		original := status.RunE
		status.RunE = func(cmd *cobra.Command, args []string) error {
			err := original(cmd, args)
			if err == nil {
				missingAuth = cmd.Annotations["authenticated"] == "false"
			}
			return err
		}
	}
	if err := root.ExecuteContext(ctx); err != nil {
		var cliErr *Error
		if !errors.As(err, &cliErr) {
			cliErr = failure("INVALID_ARGUMENT", "Invalid command or arguments; use --help.")
		}
		_ = json.NewEncoder(errOut).Encode(map[string]any{"ok": false, "error": cliErr})
		return 2
	}
	if missingAuth {
		return 3
	}
	return 0
}

// CobraCommand creates a fresh Cobra command tree for embedding in an existing
// Cobra application. Business leaf execution still passes through Runtime.Execute.
// Embedders own error rendering/exit codes; RunCLI provides SDK JSON errors.
func (r *Runtime) CobraCommand(options CLIOptions) *cobra.Command {
	root := &cobra.Command{Use: r.name, Short: r.description, Version: r.version, SilenceErrors: true, SilenceUsage: true,
		Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(func(*cobra.Command, error) error {
		return failure("INVALID_ARGUMENT", "Invalid command arguments; use --help.")
	})
	schema := &cobra.Command{Use: "schema [command path...]", Short: "Discover commands progressively.", RunE: func(cmd *cobra.Command, args []string) error {
		data, err := r.Schema(args...)
		if err != nil {
			return err
		}
		return emitJSON(cmd.OutOrStdout(), data)
	}}
	root.AddCommand(schema)
	groups := map[string]*cobra.Command{"": root}
	keys := make([]string, 0, len(r.groups))
	for key := range r.groups {
		keys = append(keys, key)
	}
	sort.Strings(keys) // A parent's key sorts before its descendants.
	for _, key := range keys {
		parts := strings.Split(key, " ")
		group := &cobra.Command{Use: parts[len(parts)-1], Short: r.groups[key], Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
		groups[strings.Join(parts[:len(parts)-1], " ")].AddCommand(group)
		groups[key] = group
	}
	keys = keys[:0]
	for key := range r.leaves {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		node := r.leaves[key]
		path := strings.Split(key, " ")
		leaf := &cobra.Command{Use: node.Name, Short: node.Description, Args: cobra.NoArgs}
		for _, option := range node.Options {
			name := strings.TrimPrefix(option.Name, "--")
			switch option.Type {
			case String:
				leaf.Flags().String(name, "", option.Description)
			case Boolean:
				leaf.Flags().Bool(name, false, option.Description)
			case Number:
				leaf.Flags().Float64(name, 0, option.Description)
			}
			if option.Default != nil && !isSensitive(option) {
				leaf.Flags().Lookup(name).DefValue = fmt.Sprint(option.Default)
			}
		}
		leaf.Flags().Bool("dry-run", false, "Preview without executing the business handler.")
		leaf.Flags().BoolP("yes", "y", false, "Confirm a REQUIRED write command.")
		leaf.Flags().String("format", "TEXT", "Output format: TEXT, JSON")
		leaf.RunE = func(cmd *cobra.Command, _ []string) error {
			args := Arguments{}
			for _, option := range node.Options {
				name := strings.TrimPrefix(option.Name, "--")
				if !cmd.Flags().Changed(name) {
					continue
				}
				switch option.Type {
				case String:
					args[option.Name], _ = cmd.Flags().GetString(name)
				case Boolean:
					args[option.Name], _ = cmd.Flags().GetBool(name)
				case Number:
					args[option.Name], _ = cmd.Flags().GetFloat64(name)
				}
			}
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			yes, _ := cmd.Flags().GetBool("yes")
			format, _ := cmd.Flags().GetString("format")
			format = strings.ToUpper(format)
			if format != "TEXT" && format != "JSON" {
				return failure("INVALID_ARGUMENT", "Format must be TEXT or JSON.")
			}
			data, err := r.Execute(cmd.Context(), path, args, ExecuteOptions{DryRun: dryRun, Confirmed: yes})
			if err != nil {
				return err
			}
			if format == "JSON" {
				data = map[string]any{"ok": true, "data": data}
			}
			return emitJSON(cmd.OutOrStdout(), data)
		}
		groups[strings.Join(path[:len(path)-1], " ")].AddCommand(leaf)
	}
	r.addAuthCommands(root, options)
	if r.skill != nil {
		group := &cobra.Command{Use: "skills", Short: "Manage business Agent Skill.", Args: cobra.NoArgs}
		group.RunE = func(cmd *cobra.Command, _ []string) error { return cmd.Help() }
		install := &cobra.Command{Use: "install", Short: "Install the business Agent Skill.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			dir, _ := cmd.Flags().GetString("dir")
			force, _ := cmd.Flags().GetBool("force")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			launcher, err := executableCommand()
			if err != nil {
				return err
			}
			result, err := r.skill.Install(SkillInstallOptions{Directory: dir, Launcher: launcher, Force: force, DryRun: dryRun})
			if err != nil {
				return err
			}
			return emitJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "data": result})
		}}
		install.Flags().String("dir", "", "Skill parent directory; default ~/.agents/skills.")
		install.Flags().Bool("force", false, "Replace this bundle's files in an existing Skill.")
		install.Flags().Bool("dry-run", false, "Preview installation without writing files.")
		group.AddCommand(install)
		root.AddCommand(group)
	}
	return root
}

func (r *Runtime) addAuthCommands(root *cobra.Command, options CLIOptions) {
	if r.auth == nil {
		return
	}
	group := &cobra.Command{Use: "auth", Short: "Manage domain credentials.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	status := &cobra.Command{Use: "status", Short: "Check local credential availability.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), r.timeout)
		defer cancel()
		status, err := r.auth.Status(ctx)
		if err != nil {
			return err
		}
		cmd.Annotations = map[string]string{"authenticated": fmt.Sprint(status.Authenticated)}
		return emitJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "data": status})
	}}
	login := &cobra.Command{Use: "login", Short: "Store a credential using configured secure input and store.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), r.timeout)
		defer cancel()
		if options.ReadSecret == nil {
			return failure("AUTH_INTERACTIVE_REQUIRED", "Use environment credentials or configure hidden terminal input and a credential store.")
		}
		secret, err := options.ReadSecret(ctx)
		if err != nil {
			return failure("AUTH_INPUT_FAILED", "Could not read credential.")
		}
		if err := r.auth.Login(ctx, secret); err != nil {
			return err
		}
		return emitJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "data": map[string]any{"domain": r.auth.config.DomainID, "authenticated": true, "message": "credential saved"}})
	}}
	logout := &cobra.Command{Use: "logout", Short: "Remove only the local stored credential.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), r.timeout)
		defer cancel()
		yes, _ := cmd.Flags().GetBool("yes")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		if dryRun {
			return emitJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "data": map[string]any{"command": "auth logout", "dryRun": true, "domain": r.auth.config.DomainID, "risk": WriteRisk()}})
		}
		if !yes {
			return failure("WRITE_APPROVAL_REQUIRED", "Deleting a local credential requires --yes.")
		}
		if err := r.auth.Logout(ctx); err != nil {
			return err
		}
		return emitJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "data": map[string]any{"domain": r.auth.config.DomainID, "message": "local credential removed; environment credentials remain managed externally"}})
	}}
	logout.Flags().BoolP("yes", "y", false, "Confirm local credential deletion.")
	logout.Flags().Bool("dry-run", false, "Preview credential deletion.")
	group.AddCommand(status, login, logout)
	root.AddCommand(group)
}

func emitJSON(out io.Writer, data any) error {
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return failure("OUTPUT_SERIALIZATION_FAILED", "Command output could not be encoded.")
	}
	if _, err := fmt.Fprintln(out, string(encoded)); err != nil {
		return failure("OUTPUT_WRITE_FAILED", "Could not write command output.")
	}
	return nil
}

func (r *Runtime) builtinSchema(path []string) (CommandSchema, error) {
	root := r.CobraCommand(CLIOptions{})
	cmd, remaining, err := root.Find(path)
	if err != nil || len(remaining) > 0 || cmd == root {
		return CommandSchema{}, failure("UNKNOWN_COMMAND", "Unknown command path.")
	}
	s := CommandSchema{Name: cmd.Name(), Description: cmd.Short}
	for _, child := range cmd.Commands() {
		if !child.Hidden {
			s.Subcommands = append(s.Subcommands, CommandSummary{child.Name(), child.Short})
		}
	}
	// Business schemas come from the SDK definitions. Builtin flag schemas come
	// directly from Cobra so help, parsing and discovery stay synchronized.
	for _, name := range []string{"yes", "dry-run", "dir", "force"} {
		if flag := cmd.Flags().Lookup(name); flag != nil {
			typeName := "boolean"
			if flag.Value.Type() == "string" {
				typeName = "String"
			}
			s.Options = append(s.Options, SchemaOption{Name: "--" + name, Type: typeName, Description: flag.Usage})
		}
	}
	return s, nil
}
