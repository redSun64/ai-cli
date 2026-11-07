package aicli

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// Group composes product modules. Even an empty group remains discoverable.
func Group(name, description string, children ...Command) Command {
	return Command{Name: name, Description: description, Children: append([]Command{}, children...)}
}

// Typed derives CLI options and input binding from a parameter struct. Metadata,
// risk, schema, Cobra and in-process execution all use the returned Command.
//
// Use exported fields tagged cli:"name[,required][,sensitive]" and help:"...".
// default:"..." declares a typed default. Anonymous structs share option sets;
// pointer fields distinguish absence from an explicitly supplied zero value.
// Definition errors are reported by New, never panicked or silently ignored.
func Typed[T, R any](command Command, run func(Invocation, T) (R, error)) Command {
	if command.Children != nil || command.Run != nil || len(command.Options) > 0 || command.input != nil {
		command.definitionError = fmt.Errorf("Typed requires a leaf without Options or Run")
		return command
	}
	if run == nil {
		command.definitionError = fmt.Errorf("Typed requires a handler")
		return command
	}
	shape, err := inputShape(reflect.TypeOf((*T)(nil)).Elem())
	if err != nil {
		command.definitionError = err
		return command
	}
	command.Options = append([]Option{}, shape.options...)
	command.input = &shape
	command.Run = func(call Invocation) (any, error) {
		value, err := shape.bind(call.Args)
		if err != nil {
			return nil, err
		}
		return run(call, value.Interface().(T))
	}
	return command
}

type inputField struct {
	index   []int
	name    string
	kind    reflect.Kind
	pointer bool
}
type shape struct {
	typ     reflect.Type
	options []Option
	fields  []inputField
}

func (s shape) matches(options []Option) bool { return reflect.DeepEqual(s.options, options) }

func (s shape) integer(name string) bool {
	for _, field := range s.fields {
		if field.name == name {
			return field.kind >= reflect.Int && field.kind <= reflect.Uint64
		}
	}
	return false
}

func inputShape(typ reflect.Type) (shape, error) {
	result := shape{typ: typ, options: []Option{}}
	if typ.Kind() != reflect.Struct {
		return result, fmt.Errorf("Typed input must be a struct")
	}
	names := map[string]bool{}
	var visit func(reflect.Type, []int) error
	visit = func(t reflect.Type, prefix []int) error {
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			tag, tagged := field.Tag.Lookup("cli")
			if tag == "-" {
				continue
			}
			index := append(append([]int{}, prefix...), i)
			if field.Anonymous && !tagged && field.Type.Kind() == reflect.Struct {
				if field.PkgPath != "" {
					return fmt.Errorf("input %s: embedded option structs must be exported", field.Name)
				}
				if err := visit(field.Type, index); err != nil {
					return err
				}
				continue
			}
			if field.PkgPath != "" {
				if tagged {
					return fmt.Errorf("input %s: CLI fields must be exported", field.Name)
				}
				continue
			}
			if !tagged || tag == "" {
				return fmt.Errorf("input %s: declare cli tag, embed an option struct, or use cli:\"-\"", field.Name)
			}
			parts := strings.Split(tag, ",")
			name := "--" + strings.TrimPrefix(parts[0], "--")
			if !optionName.MatchString(name) || globalOption(name) || names[name] {
				return fmt.Errorf("input %s: invalid, reserved or duplicate option %s", field.Name, name)
			}
			names[name] = true
			option := Option{Name: name, Description: field.Tag.Get("help")}
			seen := map[string]bool{}
			for _, attribute := range parts[1:] {
				if seen[attribute] {
					return fmt.Errorf("input %s: duplicate cli attribute", field.Name)
				}
				seen[attribute] = true
				switch attribute {
				case "required":
					option.Required = true
				case "sensitive":
					option.Sensitive = true
				default:
					return fmt.Errorf("input %s: unknown cli attribute", field.Name)
				}
			}
			base := field.Type
			pointer := base.Kind() == reflect.Pointer
			if pointer {
				base = base.Elem()
			}
			switch base.Kind() {
			case reflect.String:
				option.Type = String
			case reflect.Bool:
				option.Type = Boolean
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
				option.Type = Number
			default:
				return fmt.Errorf("input %s: unsupported CLI field type", field.Name)
			}
			binding := inputField{index: index, name: name, kind: base.Kind(), pointer: pointer}
			if raw, ok := field.Tag.Lookup("default"); ok {
				var value any
				var err error
				switch option.Type {
				case String:
					value = raw
				case Boolean:
					value, err = strconv.ParseBool(raw)
				case Number:
					value, err = strconv.ParseFloat(raw, 64)
				}
				if err != nil {
					return fmt.Errorf("input %s: default does not match field type", field.Name)
				}
				dest := reflect.New(base).Elem()
				if err = setInput(dest, value, binding); err != nil {
					return fmt.Errorf("input %s: default is out of range or not integral", field.Name)
				}
				option.Default = value
			}
			result.options = append(result.options, option)
			result.fields = append(result.fields, binding)
		}
		return nil
	}
	err := visit(typ, nil)
	return result, err
}
func (s shape) bind(args Arguments) (reflect.Value, error) {
	value := reflect.New(s.typ).Elem()
	for _, field := range s.fields {
		input := args[field.name]
		if input == nil {
			continue
		}
		target := value.FieldByIndex(field.index)
		if field.pointer {
			target.Set(reflect.New(target.Type().Elem()))
			target = target.Elem()
		}
		if err := setInput(target, input, field); err != nil {
			return reflect.Value{}, err
		}
	}
	return value, nil
}
func setInput(target reflect.Value, input any, field inputField) error {
	invalid := func() error {
		return failure("INVALID_ARGUMENT", "Invalid value for "+field.name+"; expected "+target.Type().String()+".")
	}
	switch field.kind {
	case reflect.String:
		s, ok := input.(string)
		if !ok {
			return invalid()
		}
		target.SetString(s)
	case reflect.Bool:
		b, ok := input.(bool)
		if !ok {
			return invalid()
		}
		target.SetBool(b)
	default:
		v, ok := normalize(input, Number)
		if !ok || v == nil {
			return invalid()
		}
		n := v.(float64)
		switch field.kind {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			if math.Trunc(n) != n || math.Abs(n) > 9007199254740991 || target.OverflowInt(int64(n)) {
				return invalid()
			}
			target.SetInt(int64(n))
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			if math.Trunc(n) != n || n < 0 || n > 9007199254740991 || target.OverflowUint(uint64(n)) {
				return invalid()
			}
			target.SetUint(uint64(n))
		case reflect.Float32, reflect.Float64:
			if target.OverflowFloat(n) {
				return invalid()
			}
			target.SetFloat(n)
		default:
			return invalid()
		}
	}
	return nil
}
