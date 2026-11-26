// Package apmconv converts the apm.yml of APM (Agent Package Manager) into
// a dpm.toml.
//
// Only what dpm can deploy the same way is converted: dependencies that are
// local directories or GitHub directories, deployed for the "claude"
// target. Anything else (MCP servers, other hosts, other targets, scripts)
// is reported, and nothing is generated, so that a converted manifest
// never silently drops part of apm.yml.
package apmconv

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// FileName is the name of the APM manifest.
const FileName = "apm.yml"

// DefaultTo is the destination written to [defaults] for "targets: [claude]".
const DefaultTo = "${claude}/skills"

// Error lists everything in apm.yml that cannot be converted.
type Error struct {
	// Problems are messages that start with the line in apm.yml when it is
	// known ("line 9: ..."), in the order of the file.
	Problems []string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s cannot be converted to dpm.toml:\n  %s", FileName, strings.Join(e.Problems, "\n  "))
}

// dep is one converted dependency.
type dep struct {
	// Name is the last element of the local path or of the GitHub path.
	Name string
	// Path is the local path as written in apm.yml ("" for a Git dependency).
	Path string
	// Git is the repository URL, such as "https://github.com/owner/repo".
	Git string
	// Subdir is the directory inside the repository ("" for the root).
	Subdir string
	// Ref is the part after "#" ("" for the default branch).
	Ref string
}

// Convert reads the contents of apm.yml and returns the contents of the
// equivalent dpm.toml. The output depends only on data: the dependencies
// keep the order of apm.yml. When something cannot be converted, the error
// is an *Error that lists every such item.
func Convert(data []byte) ([]byte, error) {
	deps, err := parse(data)
	if err != nil {
		return nil, err
	}
	return format(deps), nil
}

// metadataKeys are top-level keys that describe the package and do not
// affect what is deployed.
var metadataKeys = map[string]bool{
	"name":        true,
	"version":     true,
	"description": true,
	"author":      true,
	"license":     true,
}

// parse reads apm.yml and returns its dependencies.
func parse(data []byte) ([]dep, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("%s: the file is empty", FileName)
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: line %d: the top level must be a mapping", FileName, root.Line)
	}

	c := &converter{}
	hasTargets := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		switch k := key.Value; {
		case metadataKeys[k]:
		case k == "targets":
			hasTargets = true
			c.targets(value)
		case k == "dependencies":
			c.dependencies(value)
		case k == "includes":
			if !(value.Kind == yaml.ScalarNode && value.Value == "auto") && !isEmpty(value) {
				c.problem(key, `"includes" other than "auto" is not supported`)
			}
		case k == "scripts":
			if !isEmpty(value) {
				c.problem(key, `"scripts" is not supported`)
			}
		default:
			c.problem(key, fmt.Sprintf("%q is not supported", k))
		}
	}
	if !hasTargets {
		c.add(0, `"targets" is missing; only "targets: [claude]" can be converted`)
	}
	c.checkNames()
	if len(c.problems) > 0 {
		sort.SliceStable(c.problems, func(i, j int) bool { return c.problems[i].line < c.problems[j].line })
		e := &Error{}
		for _, p := range c.problems {
			e.Problems = append(e.Problems, p.String())
		}
		return nil, e
	}
	return c.deps, nil
}

// converter collects the dependencies and the problems found on the way.
type converter struct {
	deps     []dep
	lines    []int // the line of each dependency
	problems []problem
}

// problem is one item that cannot be converted. line is 0 when the item
// is not on a line (a missing key).
type problem struct {
	line int
	msg  string
}

func (p problem) String() string {
	if p.line == 0 {
		return p.msg
	}
	return fmt.Sprintf("line %d: %s", p.line, p.msg)
}

func (c *converter) add(line int, msg string) {
	c.problems = append(c.problems, problem{line, msg})
}

func (c *converter) problem(n *yaml.Node, msg string) {
	c.add(n.Line, msg)
}

// isEmpty reports whether n is null, an empty sequence or an empty mapping.
func isEmpty(n *yaml.Node) bool {
	switch n.Kind {
	case yaml.ScalarNode:
		return n.Tag == "!!null"
	case yaml.SequenceNode, yaml.MappingNode:
		return len(n.Content) == 0
	}
	return false
}

// targets accepts "targets: [claude]" and "targets: claude".
func (c *converter) targets(n *yaml.Node) {
	items := []*yaml.Node{n}
	if n.Kind == yaml.SequenceNode {
		items = n.Content
	}
	if len(items) == 0 {
		c.problem(n, `"targets" is empty; only "targets: [claude]" can be converted`)
	}
	for _, it := range items {
		if it.Kind != yaml.ScalarNode || it.Value != "claude" {
			c.problem(it, fmt.Sprintf("target %s is not supported; only \"claude\" can be converted", nodeText(it)))
		}
	}
}

func (c *converter) dependencies(n *yaml.Node) {
	if isEmpty(n) {
		return
	}
	if n.Kind != yaml.MappingNode {
		c.problem(n, `"dependencies" must be a mapping`)
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i], n.Content[i+1]
		switch key.Value {
		case "apm":
			c.apmDeps(value)
		case "mcp":
			if isEmpty(value) {
				continue
			}
			if value.Kind != yaml.SequenceNode {
				c.problem(value, "MCP servers are not supported")
				continue
			}
			for _, it := range value.Content {
				c.problem(it, fmt.Sprintf("MCP server %s is not supported", nodeText(it)))
			}
		default:
			c.problem(key, fmt.Sprintf("dependencies of type %q are not supported", key.Value))
		}
	}
}

func (c *converter) apmDeps(n *yaml.Node) {
	if isEmpty(n) {
		return
	}
	if n.Kind != yaml.SequenceNode {
		c.problem(n, `"dependencies.apm" must be a list`)
		return
	}
	for _, it := range n.Content {
		if it.Kind != yaml.ScalarNode || it.Tag != "!!str" {
			c.problem(it, fmt.Sprintf("dependency %s is not supported; only strings are", nodeText(it)))
			continue
		}
		d, err := parseDep(it.Value)
		if err != nil {
			c.problem(it, fmt.Sprintf("dependency %q: %v", it.Value, err))
			continue
		}
		c.deps = append(c.deps, d)
		c.lines = append(c.lines, it.Line)
	}
}

// checkNames reports names that dpm does not accept and names used twice,
// ignoring letter case.
func (c *converter) checkNames() {
	// Names that differ only in letter case would be deployed to the same
	// directory on Windows and macOS, which dpm rejects on every OS.
	first := map[string]int{}
	for i, d := range c.deps {
		if strings.HasPrefix(d.Name, "_") {
			c.add(c.lines[i], fmt.Sprintf("the name %q starts with \"_\", which dpm reserves for local files", d.Name))
		}
		folded := strings.ToLower(d.Name)
		if j, ok := first[folded]; ok {
			c.add(c.lines[i], fmt.Sprintf("the name %q is also used on line %d", d.Name, c.lines[j]))
			continue
		}
		first[folded] = i
	}
}

// nodeText returns a short form of n for messages.
func nodeText(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return fmt.Sprintf("%q", n.Value)
	}
	out, err := yaml.Marshal(n)
	if err != nil {
		return "(unknown)"
	}
	s := strings.Join(strings.Fields(string(out)), " ")
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return fmt.Sprintf("%q", s)
}

const githubPrefix = "github.com/"

// parseDep converts one entry of dependencies.apm.
func parseDep(s string) (dep, error) {
	switch {
	case s == "":
		return dep{}, errors.New("empty")
	case isLocal(s):
		return parseLocal(s)
	case strings.HasPrefix(s, githubPrefix):
		return parseGitHub(strings.TrimPrefix(s, githubPrefix))
	}
	return dep{}, errors.New("only local paths and github.com/<owner>/<repo>[/<subdir>][#<ref>] can be converted")
}

// isLocal reports whether s is written as a local path: it starts with
// "./", "../", "~", "/" or a drive letter ("\" is accepted for "/").
func isLocal(s string) bool {
	t := strings.ReplaceAll(s, `\`, "/")
	switch {
	case t == "." || t == ".." || t == "~":
		return true
	case strings.HasPrefix(t, "./"), strings.HasPrefix(t, "../"), strings.HasPrefix(t, "~/"), strings.HasPrefix(t, "/"):
		return true
	}
	return len(t) >= 2 && t[1] == ':' && isASCIILetter(t[0])
}

func isASCIILetter(c byte) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func parseLocal(s string) (dep, error) {
	t := strings.ReplaceAll(s, `\`, "/")
	if strings.HasPrefix(t, "/") || (len(t) >= 2 && t[1] == ':') {
		return dep{}, errors.New("an absolute path cannot be used in dpm.toml; use a path relative to apm.yml or a path starting with ~")
	}
	if strings.Contains(s, "${") {
		return dep{}, errors.New(`"${" would be read as a variable in dpm.toml`)
	}
	elems := strings.FieldsFunc(t, func(r rune) bool { return r == '/' })
	name := ""
	if len(elems) > 0 {
		name = elems[len(elems)-1]
	}
	switch name {
	case "", ".", "..", "~":
		return dep{}, errors.New("the path does not end with a directory name, which dpm uses as the dependency name")
	}
	return dep{Name: name, Path: s}, nil
}

func parseGitHub(s string) (dep, error) {
	p, ref, hasRef := strings.Cut(s, "#")
	if hasRef && !validRef(ref) {
		return dep{}, fmt.Errorf("%q is not a valid tag, branch or commit", ref)
	}
	elems := strings.Split(strings.TrimSuffix(p, "/"), "/")
	if len(elems) < 2 {
		return dep{}, errors.New("the repository must be written as github.com/<owner>/<repo>")
	}
	for _, e := range elems {
		if e == "" || e == "." || e == ".." || strings.ContainsAny(e, "\\\x00\n\r\t ") {
			return dep{}, fmt.Errorf("%q is not a valid path element", e)
		}
	}
	d := dep{
		Git:    "https://github.com/" + elems[0] + "/" + elems[1],
		Subdir: strings.Join(elems[2:], "/"),
		Ref:    ref,
		Name:   elems[len(elems)-1],
	}
	return d, nil
}

// validRef matches what dpm accepts for "ref": one tag, branch or commit,
// not a refspec.
func validRef(ref string) bool {
	return ref != "" && !strings.ContainsAny(ref, ":* \t\n\r\x00") && !strings.HasPrefix(ref, "+")
}

// format returns the dpm.toml for deps: [defaults] with DefaultTo and one
// [deps.<name>] table per dependency, in the given order.
func format(deps []dep) []byte {
	var b strings.Builder
	b.WriteString("# Generated by dpm from apm.yml.\n")
	b.WriteString("\n[defaults]\n")
	fmt.Fprintf(&b, "to = [%s]\n", quote(DefaultTo))
	for _, d := range deps {
		fmt.Fprintf(&b, "\n[deps.%s]\n", key(d.Name))
		if d.Git == "" {
			fmt.Fprintf(&b, "path = %s\n", quote(d.Path))
			continue
		}
		fmt.Fprintf(&b, "git = %s\n", quote(d.Git))
		if d.Subdir != "" {
			fmt.Fprintf(&b, "subdir = %s\n", quote(d.Subdir))
		}
		if d.Ref != "" {
			fmt.Fprintf(&b, "ref = %s\n", quote(d.Ref))
		}
	}
	return []byte(b.String())
}

// key returns name as a TOML key: bare when it can be, quoted otherwise.
func key(name string) string {
	for _, r := range name {
		if !(r == '-' || r == '_' || ('0' <= r && r <= '9') || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z')) {
			return quote(name)
		}
	}
	return name
}

// quote returns s as a TOML basic string.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
