package incidentmemory

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const (
	defaultMaxCases       = 3
	defaultMaxRenderBytes = 4096
)

type Query struct {
	AffectedService string
	Symptoms        []string
	Signals         []string
	Tags            []string
	Text            string
}

type RetrieveOptions struct {
	IncludeCandidates bool
	IncludeRejected   bool
	Limit             int
}

type SimilarCase struct {
	Card            Card     `json:"card"`
	Score           int      `json:"score"`
	Reasons         []string `json:"reasons"`
	DifferenceNotes string   `json:"difference_notes,omitempty"`
}

func QueryFromText(text string) Query {
	return Query{Text: text}
}

func (s *Store) Retrieve(q Query, opts RetrieveOptions) ([]SimilarCase, error) {
	cards, err := s.List()
	if err != nil {
		return nil, err
	}
	if opts.Limit <= 0 {
		opts.Limit = defaultMaxCases
	}

	var out []SimilarCase
	for _, c := range cards {
		if c.Status == StatusRejected && !opts.IncludeRejected {
			continue
		}
		if c.Status == StatusCandidate && !opts.IncludeCandidates {
			continue
		}
		if c.Status != StatusApproved && !opts.IncludeCandidates && !opts.IncludeRejected {
			continue
		}
		scored := scoreCase(c, q)
		if scored.Score == 0 {
			continue
		}
		out = append(out, scored)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Card.CreatedAt.After(out[j].Card.CreatedAt)
		}
		return out[i].Score > out[j].Score
	})
	if len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return out, nil
}

func scoreCase(c Card, q Query) SimilarCase {
	var score int
	var reasons []string
	service := strings.ToLower(strings.TrimSpace(q.AffectedService))
	if service != "" && service == strings.ToLower(c.AffectedService) {
		score += 5
		reasons = append(reasons, "same affected service")
	}

	queryTokens := tokenSet(append(append(append(q.Symptoms, q.Signals...), q.Tags...), q.Text))
	addOverlap := func(label string, values []string, weight int) {
		overlap := overlapTokens(queryTokens, tokenSet(values))
		if len(overlap) == 0 {
			return
		}
		score += len(overlap) * weight
		reasons = append(reasons, fmt.Sprintf("%s overlap: %s", label, strings.Join(overlap, ", ")))
	}
	addOverlap("symptom", c.Symptoms, 3)
	addOverlap("signal", c.Signals, 2)
	addOverlap("tag", c.Tags, 1)
	addOverlap("service", []string{c.AffectedService}, 2)

	return SimilarCase{
		Card:            c,
		Score:           score,
		Reasons:         reasons,
		DifferenceNotes: c.WhatWasDifferent,
	}
}

func tokenSet(values []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, v := range values {
		for _, tok := range strings.FieldsFunc(strings.ToLower(v), func(r rune) bool {
			return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_')
		}) {
			if len(tok) < 3 {
				continue
			}
			out[tok] = struct{}{}
		}
	}
	return out
}

func overlapTokens(a, b map[string]struct{}) []string {
	var out []string
	for tok := range a {
		if _, ok := b[tok]; ok {
			out = append(out, tok)
		}
	}
	sort.Strings(out)
	return out
}
