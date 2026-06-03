package incidentmemory

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	StatusCandidate = "candidate"
	StatusApproved  = "approved"
	StatusRejected  = "rejected"

	CauseSuspected = "suspected"
	CauseConfirmed = "confirmed"
)

// Source identifies where a case card came from. Type is usually "session" or
// "postmortem"; Path may point at the local session log or imported writeup.
type Source struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	Path string `json:"path,omitempty"`
	Ref  string `json:"ref,omitempty"`
}

// Card is a structured, operator-reviewed prior incident example.
type Card struct {
	ID               string    `json:"id"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Status           string    `json:"status"`
	Symptoms         []string  `json:"symptoms"`
	AffectedService  string    `json:"affected_service"`
	Signals          []string  `json:"signals"`
	CauseStatus      string    `json:"cause_status"`
	Cause            string    `json:"cause"`
	FixOrMitigation  string    `json:"fix_or_mitigation"`
	WhatWasDifferent string    `json:"what_was_different"`
	Source           Source    `json:"source"`
	Confidence       float64   `json:"confidence"`
	Tags             []string  `json:"tags,omitempty"`
}

// Validate checks the core schema before a card can enter durable storage.
func Validate(c Card) error {
	var problems []string
	if len(cleanList(c.Symptoms)) == 0 {
		problems = append(problems, "symptoms required")
	}
	if strings.TrimSpace(c.AffectedService) == "" {
		problems = append(problems, "affected_service required")
	}
	if strings.TrimSpace(c.Source.Type) == "" || (strings.TrimSpace(c.Source.ID) == "" && strings.TrimSpace(c.Source.Path) == "" && strings.TrimSpace(c.Source.Ref) == "") {
		problems = append(problems, "source required")
	}
	switch c.CauseStatus {
	case CauseSuspected, CauseConfirmed:
	case "":
		problems = append(problems, "cause_status required")
	default:
		problems = append(problems, "cause_status must be suspected or confirmed")
	}
	if c.Confidence <= 0 || c.Confidence > 1 {
		problems = append(problems, "confidence must be between 0 and 1")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func normalizeCard(c Card) Card {
	c.Status = strings.TrimSpace(c.Status)
	if c.Status == "" {
		c.Status = StatusCandidate
	}
	c.Symptoms = cleanList(c.Symptoms)
	c.AffectedService = cleanText(c.AffectedService)
	c.Signals = cleanList(c.Signals)
	c.CauseStatus = cleanText(c.CauseStatus)
	c.Cause = cleanText(c.Cause)
	c.FixOrMitigation = cleanText(c.FixOrMitigation)
	c.WhatWasDifferent = cleanText(c.WhatWasDifferent)
	c.Source.Type = cleanText(c.Source.Type)
	c.Source.ID = cleanText(c.Source.ID)
	c.Source.Path = cleanText(c.Source.Path)
	c.Source.Ref = cleanText(c.Source.Ref)
	c.Tags = cleanList(c.Tags)
	return c
}

func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, v := range in {
		v = cleanText(v)
		if v == "" {
			continue
		}
		key := strings.ToLower(v)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, v)
	}
	return out
}

func cleanText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func validateStatus(status string) error {
	switch status {
	case StatusCandidate, StatusApproved, StatusRejected:
		return nil
	default:
		return fmt.Errorf("invalid status %q", status)
	}
}
