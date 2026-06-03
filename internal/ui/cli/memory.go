package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/rlaope/cloudy/internal/incidentmemory"
)

func init() { Register(&memoryCmd{}) }

type memoryCmd struct{}

func (memoryCmd) Name() string  { return "memory" }
func (memoryCmd) Short() string { return `inspect and approve local memory` }

type memoryOptions struct {
	base             baseFlags
	includeRejected  bool
	includeCandidate bool
}

func (o *memoryOptions) bind(fs *flagSet) {
	o.base.bind(fs.FlagSet)
	fs.BoolVar(&o.includeRejected, "include-rejected", false, "include rejected incident cases")
	fs.BoolVar(&o.includeCandidate, "include-candidates", true, "include candidate incident cases")
}

func (memoryCmd) Run(_ context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printMemoryHelp(stdout)
		return nil
	}
	if args[0] != "cases" {
		return errf("memory: unknown subcommand: %s", args[0])
	}
	return runMemoryCases(args[1:], stdout, stderr)
}

func runMemoryCases(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printMemoryCasesHelp(stdout)
		return nil
	}
	sub := args[0]
	rest := args[1:]
	store := incidentmemory.NewDefaultStore()

	switch sub {
	case "list":
		var opts memoryOptions
		pos, err := parseInto(&opts, "memory cases list", rest, stderr)
		if err != nil {
			return err
		}
		if len(pos) > 0 {
			return errf("unexpected memory cases list argument: %s", pos[0])
		}
		cards, err := store.List()
		if err != nil {
			return errf("memory cases list: %w", err)
		}
		cards = filterMemoryCards(cards, opts.includeCandidate, opts.includeRejected)
		if opts.base.asJSON {
			return writeJSON(stdout, cards)
		}
		for _, c := range cards {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%.2f\t%s\n", c.ID, c.Status, c.AffectedService, c.Confidence, strings.Join(c.Symptoms, "; "))
		}
		return nil
	case "show":
		var opts memoryOptions
		pos, err := parseInto(&opts, "memory cases show", reorderMemoryCaseIDArgs(rest), stderr)
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return errf("usage: cloudy memory cases show <id>")
		}
		c, err := store.Get(pos[0])
		if err != nil {
			return errf("memory cases show: %w", err)
		}
		if opts.base.asJSON {
			return writeJSON(stdout, c)
		}
		printCard(stdout, c)
		return nil
	case "approve", "reject":
		var opts memoryOptions
		pos, err := parseInto(&opts, "memory cases "+sub, reorderMemoryCaseIDArgs(rest), stderr)
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return errf("usage: cloudy memory cases %s <id>", sub)
		}
		var c incidentmemory.Card
		if sub == "approve" {
			c, err = store.Approve(pos[0])
		} else {
			c, err = store.Reject(pos[0])
		}
		if err != nil {
			return errf("memory cases %s: %w", sub, err)
		}
		if opts.base.asJSON {
			return writeJSON(stdout, c)
		}
		fmt.Fprintf(stdout, "%s %s\n", c.ID, c.Status)
		return nil
	default:
		return errf("memory cases: unknown subcommand: %s", sub)
	}
}

func filterMemoryCards(cards []incidentmemory.Card, includeCandidate, includeRejected bool) []incidentmemory.Card {
	out := make([]incidentmemory.Card, 0, len(cards))
	for _, c := range cards {
		if c.Status == incidentmemory.StatusRejected && !includeRejected {
			continue
		}
		if c.Status == incidentmemory.StatusCandidate && !includeCandidate {
			continue
		}
		out = append(out, c)
	}
	return out
}

func reorderMemoryCaseIDArgs(args []string) []string {
	if len(args) <= 1 {
		return args
	}
	flags := make([]string, 0, len(args))
	pos := make([]string, 0, 1)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--context" || arg == "--kubeconfig":
			flags = append(flags, arg)
			if i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
		case strings.HasPrefix(arg, "--context=") || strings.HasPrefix(arg, "--kubeconfig="):
			flags = append(flags, arg)
		case strings.HasPrefix(arg, "-"):
			flags = append(flags, arg)
		default:
			pos = append(pos, arg)
		}
	}
	return append(flags, pos...)
}

func printCard(w io.Writer, c incidentmemory.Card) {
	fmt.Fprintf(w, "id: %s\nstatus: %s\nservice: %s\nconfidence: %.2f\n", c.ID, c.Status, c.AffectedService, c.Confidence)
	fmt.Fprintf(w, "symptoms: %s\nsignals: %s\n", strings.Join(c.Symptoms, "; "), strings.Join(c.Signals, "; "))
	fmt.Fprintf(w, "cause_status: %s\ncause: %s\nfix_or_mitigation: %s\nwhat_was_different: %s\n", c.CauseStatus, c.Cause, c.FixOrMitigation, c.WhatWasDifferent)
	fmt.Fprintf(w, "source: %s %s %s\n", c.Source.Type, c.Source.ID, c.Source.Ref)
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printMemoryHelp(w io.Writer) {
	fmt.Fprintln(w, `usage:
  cloudy memory cases <list|show|approve|reject> [args]`)
}

func printMemoryCasesHelp(w io.Writer) {
	fmt.Fprintln(w, `usage:
  cloudy memory cases list [--json] [--include-rejected]
  cloudy memory cases show <id> [--json]
  cloudy memory cases approve <id> [--json]
  cloudy memory cases reject <id> [--json]`)
}
