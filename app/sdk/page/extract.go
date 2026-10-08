package page

import (
	"fmt"
	"maps"
	"slices"
	"text/template/parse"

	"github.com/jroedel/reconcile/business/domain/translation/translationbus"
)

// extract reads every {{t}} and {{tc}} out of the parsed templates, pages and
// mail alike: the strings the app speaks, for translationbus.Register. sets
// is each page's or message's parse trees, by its name.
//
// From the parse trees rather than by running the templates, so that a string
// on a page nobody has opened since the deploy, or behind an {{if}} that is
// rarely true, is still waiting to be translated. And it is where the rule
// that keeps that true is enforced: the English, the context and every
// placeholder's name must be a literal string. {{t .Message}} cannot be
// registered, so it is a startup error rather than a string that is never
// translated.
//
// Each string is returned with the template files it is written in -- its
// tree's ParseName, so a string in the layout or a partial is the layout's
// or the partial's, and not every page's that includes it. That is the
// "where" a translator reads to know whether "Close" is a button.
func extract(sets map[string][]*parse.Tree) ([]translationbus.Use, error) {
	found := &finds{in: map[translationbus.Source]map[string]bool{}}

	for _, name := range slices.Sorted(maps.Keys(sets)) {
		for _, tree := range sets[name] {
			if tree == nil || tree.Root == nil {
				continue
			}

			found.file = tree.ParseName
			if found.file == "" {
				found.file = name
			}

			if err := walk(tree.Root, found); err != nil {
				return nil, fmt.Errorf("%s, in %s: %w", name, tree.Name, err)
			}
		}
	}

	out := make([]translationbus.Use, 0, len(found.in))
	for src, files := range found.in {
		out = append(out, translationbus.Use{Source: src, Pages: slices.Sorted(maps.Keys(files))})
	}

	slices.SortFunc(out, func(a, b translationbus.Use) int {
		if a.EN != b.EN {
			if a.EN < b.EN {
				return -1
			}

			return 1
		}

		if a.Context < b.Context {
			return -1
		}

		if a.Context > b.Context {
			return 1
		}

		return 0
	})

	return out, nil
}

func walk(n parse.Node, found *finds) error {
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return nil
		}

		for _, c := range n.Nodes {
			if err := walk(c, found); err != nil {
				return err
			}
		}
	case *parse.ActionNode:
		return walk(n.Pipe, found)
	case *parse.IfNode:
		return branch(&n.BranchNode, found)
	case *parse.RangeNode:
		return branch(&n.BranchNode, found)
	case *parse.WithNode:
		return branch(&n.BranchNode, found)
	case *parse.TemplateNode:
		return walk(n.Pipe, found)
	case *parse.PipeNode:
		if n == nil {
			return nil
		}

		for _, cmd := range n.Cmds {
			if err := command(cmd, found); err != nil {
				return err
			}
		}
	}

	return nil
}

func branch(b *parse.BranchNode, found *finds) error {
	for _, n := range []parse.Node{b.Pipe, b.List, b.ElseList} {
		if err := walk(n, found); err != nil {
			return err
		}
	}

	return nil
}

// command records a t or tc call, and walks into the arguments of anything
// else: {{if eq (t "Yes") .X}} hides a call inside a parenthesised pipe.
func command(cmd *parse.CommandNode, found *finds) error {
	if len(cmd.Args) > 0 {
		if id, ok := cmd.Args[0].(*parse.IdentifierNode); ok && (id.Ident == "t" || id.Ident == "tc") {
			src, err := call(id.Ident, cmd.Args[1:])
			if err != nil {
				return err
			}

			found.add(src)
		}
	}

	for _, arg := range cmd.Args {
		if err := walk(arg, found); err != nil {
			return err
		}
	}

	return nil
}

// call reads one t or tc call's literal arguments.
func call(fn string, args []parse.Node) (translationbus.Source, error) {
	var src translationbus.Source

	literal := func(i int, what string) (string, error) {
		if i >= len(args) {
			return "", fmt.Errorf("%s needs %s", fn, what)
		}

		s, ok := args[i].(*parse.StringNode)
		if !ok {
			return "", fmt.Errorf("%s's %s must be a literal string, not %s, so that it can be found and translated", fn, what, args[i])
		}

		return s.Text, nil
	}

	first := 0

	if fn == "tc" {
		c, err := literal(0, "context")
		if err != nil {
			return src, err
		}

		src.Context = c
		first = 1
	}

	en, err := literal(first, "English text")
	if err != nil {
		return src, err
	}

	src.EN = en

	// The placeholders' names, which are every other argument after the
	// text. Their values may be anything.
	for i := first + 1; i < len(args); i += 2 {
		if _, err := literal(i, "placeholder name"); err != nil {
			return src, fmt.Errorf("in %q: %w", en, err)
		}
	}

	return src, nil
}

// finds is the strings found so far, each with the files it is in, and the
// file being read.
type finds struct {
	file string
	in   map[translationbus.Source]map[string]bool
}

func (f *finds) add(src translationbus.Source) {
	if f.in[src] == nil {
		f.in[src] = map[string]bool{}
	}

	f.in[src][f.file] = true
}
