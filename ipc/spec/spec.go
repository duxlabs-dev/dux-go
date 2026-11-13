// Package spec describes an app's IPC methods for `describe`, `ipc list` and shell completion, and parses
// the `ipc <method> key=value...` command line into request params.
package spec

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

type Param struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	// List params take a list of strings; the CLI wraps a single value.
	List bool   `json:"list,omitempty"`
	Desc string `json:"desc,omitempty"`
}

type Method struct {
	Name   string  `json:"name"`
	Group  string  `json:"group"`
	Desc   string  `json:"desc,omitempty"`
	Params []Param `json:"params,omitempty"`
}

func Req(name, desc string) Param { return Param{Name: name, Required: true, Desc: desc} }

func Opt(name, desc string) Param { return Param{Name: name, Desc: desc} }

func OptList(name, desc string) Param { return Param{Name: name, List: true, Desc: desc} }

type Methods []Method

// Names returns the method names sorted.
func (m Methods) Names() []string {
	names := make([]string, len(m))
	for i, method := range m {
		names[i] = method.Name
	}
	slices.Sort(names)
	return names
}

func (m Methods) Find(name string) (Method, bool) {
	for _, method := range m {
		if method.Name == name {
			return method, true
		}
	}
	return Method{}, false
}

// Complete is a cobra ValidArgsFunction for `ipc <method> [key=value...]`.
func (m Methods) Complete(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return m.Names(), cobra.ShellCompDirectiveNoFileComp
	}
	method, ok := m.Find(args[0])
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	used := make(map[string]bool, len(args))
	for _, arg := range args[1:] {
		if key, _, found := strings.Cut(arg, "="); found {
			used[key] = true
		}
	}

	comps := make([]string, 0, len(method.Params))
	for _, p := range method.Params {
		if used[p.Name] && !p.List {
			continue
		}
		desc := p.Desc
		if p.Required {
			desc = strings.TrimSpace("required " + desc)
		}
		comps = append(comps, fmt.Sprintf("%s=\t%s", p.Name, desc))
	}
	return comps, cobra.ShellCompDirectiveNoSpace | cobra.ShellCompDirectiveNoFileComp
}

// ParseParams turns key=value args into params with string values. A key given twice, a value written as a
// JSON array, or a value for one of m's List params becomes a list. m may be the zero Method.
func (m Method) ParseParams(args []string) (map[string]any, error) {
	return m.parse(args, func(raw string) any { return raw })
}

// ParseJSONParams is ParseParams, except that a value which parses as JSON is sent typed (numbers, bools,
// objects). Items of a List param stay strings, so a path like 2024 is not sent as a number.
func (m Method) ParseJSONParams(args []string) (map[string]any, error) {
	return m.parse(args, func(raw string) any {
		var value any
		if json.Unmarshal([]byte(raw), &value) != nil {
			return raw
		}
		return value
	})
}

func (m Method) parse(args []string, decode func(string) any) (map[string]any, error) {
	if len(args) == 0 {
		return nil, nil
	}
	lists := make(map[string]bool, len(m.Params))
	for _, p := range m.Params {
		lists[p.Name] = p.List
	}
	params := make(map[string]any, len(args))
	for _, arg := range args {
		key, raw, ok := strings.Cut(arg, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("expected key=value, got %q", arg)
		}
		var list []any
		var value any
		isList := strings.HasPrefix(strings.TrimLeft(raw, " \t\r\n"), "[") && json.Unmarshal([]byte(raw), &list) == nil
		switch {
		case isList:
		case lists[key]:
			list, isList = []any{raw}, true
		default:
			value = decode(raw)
		}
		prev, seen := params[key]
		switch {
		case seen && isList:
			params[key] = append(asList(prev), list...)
		case seen:
			params[key] = append(asList(prev), value)
		case isList:
			params[key] = list
		default:
			params[key] = value
		}
	}
	return params, nil
}

func asList(value any) []any {
	if list, ok := value.([]any); ok {
		return list
	}
	return []any{value}
}
