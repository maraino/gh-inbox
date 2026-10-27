package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"go.yaml.in/yaml/v3"

	"github.com/maraino/gh-inbox/internal/action"
	"github.com/maraino/gh-inbox/internal/cache"
	"github.com/maraino/gh-inbox/internal/filter"
	"github.com/maraino/gh-inbox/internal/inbox"
)

// rule is a named set of filters and the action to apply to the
// notifications that match them.
type rule struct {
	name    string
	kind    action.Kind
	filters filter.Options
}

// rulesFile is the format of the rules file.
type rulesFile struct {
	Rules []ruleEntry `yaml:"rules"`
}

type ruleEntry struct {
	Name   string `yaml:"name"`
	Action string `yaml:"action"`
	// Filters maps filter flags, without the leading --, to their values:
	// a string, a list, or a boolean.
	Filters map[string]yaml.Node `yaml:"filters"`
}

// defaultRulesPath returns where the rules are kept:
// $XDG_CONFIG_HOME/gh-inbox/rules.yml, or ~/.config/gh-inbox/rules.yml.
func defaultRulesPath() (string, error) {
	return cache.XDGPath("XDG_CONFIG_HOME", ".config", "gh-inbox", "rules.yml")
}

// loadRules reads the rules file at path.
func loadRules(path string) ([]rule, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no rules file at %s: see \"gh inbox clean --help\" for its format", path)
	}
	if err != nil {
		return nil, err
	}
	rules, err := parseRules(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return rules, nil
}

// parseRules parses and checks the rules in a rules file.
func parseRules(data []byte) ([]rule, error) {
	var f rulesFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(f.Rules) == 0 {
		return nil, errors.New("no rules")
	}

	rules := make([]rule, 0, len(f.Rules))
	seen := make(map[string]bool)
	for i, e := range f.Rules {
		if e.Name == "" {
			return nil, fmt.Errorf("rule %d has no name", i+1)
		}
		if seen[e.Name] {
			return nil, fmt.Errorf("rule %q appears twice", e.Name)
		}
		seen[e.Name] = true
		r, err := e.rule()
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", e.Name, err)
		}
		rules = append(rules, r)
	}
	return rules, nil
}

func (e *ruleEntry) rule() (rule, error) {
	r := rule{name: e.Name, kind: action.Done}
	if e.Action != "" {
		kind, err := action.ParseKind(e.Action)
		if err != nil {
			return rule{}, err
		}
		r.kind = kind
	}

	// The filters take the same names and values as the flags, so they're
	// parsed by the flags.
	fs := pflag.NewFlagSet(e.Name, pflag.ContinueOnError)
	addFilterFlags(fs, &r.filters)
	for name, node := range e.Filters {
		if fs.Lookup(name) == nil {
			return rule{}, fmt.Errorf("unknown filter %q", name)
		}
		values, err := filterValues(&node)
		if err != nil {
			return rule{}, fmt.Errorf("filter %s: %w", name, err)
		}
		for _, v := range values {
			if err := fs.Set(name, v); err != nil {
				return rule{}, fmt.Errorf("filter %s: %w", name, err)
			}
		}
	}
	if r.filters.IsEmpty() {
		return rule{}, errors.New("no filters: a rule must not act on every notification")
	}

	// Check the values now, so that a mistake in one rule stops every rule
	// before anything is done. @me needs a user to check against, but any
	// will do.
	if _, err := filter.New(&r.filters, filter.Env{Me: inbox.Identity{Login: "me"}}); err != nil {
		return rule{}, err
	}
	return r, nil
}

// filterValues returns the values of a filter: a scalar, or a list of them.
func filterValues(node *yaml.Node) ([]string, error) {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag == "!!null" {
			return nil, errors.New("no value")
		}
		return []string{node.Value}, nil
	case yaml.SequenceNode:
		values := make([]string, 0, len(node.Content))
		for _, n := range node.Content {
			if n.Kind != yaml.ScalarNode {
				return nil, errors.New("want a value or a list of values")
			}
			values = append(values, n.Value)
		}
		return values, nil
	default:
		return nil, errors.New("want a value or a list of values")
	}
}

// ruleFlags are the flags that choose rules from the rules file.
type ruleFlags struct {
	names []string
	all   bool
	file  string
}

// add adds the flags to fs, with the help of --rule and --rules.
func (f *ruleFlags) add(fs *pflag.FlagSet, ruleHelp, rulesHelp string) {
	fs.StringSliceVar(&f.names, "rule", nil, ruleHelp)
	fs.BoolVar(&f.all, "rules", false, rulesHelp)
	fs.StringVar(&f.file, "rules-file", "", "Read the rules from `file` (default: ~/.config/gh-inbox/rules.yml)")
}

// given reports whether rules were chosen.
func (f *ruleFlags) given() bool {
	return len(f.names) > 0 || f.all
}

// load reads the rules file and returns the rules chosen.
func (f *ruleFlags) load(e *env) ([]rule, error) {
	if len(f.names) > 0 && f.all {
		return nil, errors.New("--rule and --rules can't be used together")
	}
	path := f.file
	if path == "" {
		var err error
		if path, err = e.rulesPath(); err != nil {
			return nil, err
		}
	}
	rules, err := loadRules(path)
	if err != nil {
		return nil, err
	}
	return selectRules(rules, f.names)
}

// ruleFilters returns the filters of each rule.
func ruleFilters(rules []rule) []*filter.Options {
	filters := make([]*filter.Options, len(rules))
	for i := range rules {
		filters[i] = &rules[i].filters
	}
	return filters
}

// selectRules returns the rules named, in the order of the file, or every
// rule when names is empty.
func selectRules(rules []rule, names []string) ([]rule, error) {
	if len(names) == 0 {
		return rules, nil
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	var selected []rule
	for _, r := range rules {
		if want[r.name] {
			selected = append(selected, r)
			delete(want, r.name)
		}
	}
	if len(want) > 0 {
		var missing []string
		for _, n := range names {
			if want[n] {
				missing = append(missing, strconv.Quote(n))
				want[n] = false
			}
		}
		return nil, fmt.Errorf("no rule named %s", strings.Join(missing, ", "))
	}
	return selected, nil
}
