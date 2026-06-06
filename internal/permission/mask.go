package permission

import (
	"encoding/json"
	"fmt"
	"regexp"
)

const redacted = "[REDACTED]"

// Masker applies field-level redaction to strings, byte slices, and JSON
// documents. Construct one via NewMasker; a nil *Masker is safe to call on
// any method (all methods are no-ops when the receiver is nil).
type Masker struct {
	keyREs   []*regexp.Regexp
	valueREs []*regexp.Regexp
}

// NewMasker compiles the regexes in p.Masking and returns a ready-to-use
// *Masker. Returns (nil, nil) when p is nil or both regex lists are empty —
// callers can treat a nil *Masker as "no masking needed". Returns a non-nil
// error when any regex fails to compile.
func NewMasker(p *Profile) (*Masker, error) {
	if p == nil {
		return nil, nil
	}
	m := p.Masking
	if len(m.KeyRegex) == 0 && len(m.ValueRegex) == 0 {
		return nil, nil
	}

	masker := &Masker{}

	for _, pat := range m.KeyRegex {
		re, err := regexp.Compile("(?i)" + pat)
		if err != nil {
			return nil, fmt.Errorf("permission: masking key_regex %q: %w", pat, err)
		}
		masker.keyREs = append(masker.keyREs, re)
	}

	for _, pat := range m.ValueRegex {
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("permission: masking value_regex %q: %w", pat, err)
		}
		masker.valueREs = append(masker.valueREs, re)
	}

	return masker, nil
}

// MaskString returns a copy of s with every ValueRegex match replaced by
// "[REDACTED]". Returns s unchanged when the receiver is nil.
func (m *Masker) MaskString(s string) string {
	if m == nil {
		return s
	}
	for _, re := range m.valueREs {
		s = re.ReplaceAllString(s, redacted)
	}
	return s
}

// MaskBytes is the []byte equivalent of MaskString. Returns b unchanged
// (not a copy) when the receiver is nil or no patterns match.
func (m *Masker) MaskBytes(b []byte) []byte {
	if m == nil {
		return b
	}
	for _, re := range m.valueREs {
		b = re.ReplaceAll(b, []byte(redacted))
	}
	return b
}

// MaskJSON recursively walks a JSON document, applying KeyRegex and
// ValueRegex redaction. Non-JSON input is returned as-is with a nil error
// so callers can blanket-mask all observations without conditional logic.
// Returns the original bytes and a nil error when the receiver is nil.
func (m *Masker) MaskJSON(b []byte) ([]byte, error) {
	if m == nil {
		return b, nil
	}

	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		// Not valid JSON — return original bytes, nil error (per spec).
		return b, nil
	}

	doc = m.maskValue("", doc)

	out, err := json.Marshal(doc)
	if err != nil {
		return b, fmt.Errorf("permission: masking re-encode: %w", err)
	}
	return out, nil
}

// MaskMap applies KeyRegex and ValueRegex redaction to a map in-place.
// No-op when the receiver is nil.
func (m *Masker) MaskMap(v map[string]any) {
	if m == nil {
		return
	}
	for k, val := range v {
		v[k] = m.maskValue(k, val)
	}
}

// maskValue is the recursive core. key is the parent key (empty for top-level
// array elements). It handles objects, arrays, and scalar values.
func (m *Masker) maskValue(key string, v any) any {
	// If the key itself matches a KeyRegex, redact the entire value regardless
	// of its type.
	if key != "" && m.keyMatches(key) {
		return redacted
	}

	switch typed := v.(type) {
	case map[string]any:
		for k, child := range typed {
			typed[k] = m.maskValue(k, child)
		}
		return typed

	case []any:
		for i, elem := range typed {
			typed[i] = m.maskValue("", elem)
		}
		return typed

	case string:
		return m.MaskString(typed)

	default:
		// Numbers, booleans, nil — only string values can contain secrets.
		return v
	}
}

// keyMatches returns true when key matches any compiled KeyRegex.
func (m *Masker) keyMatches(key string) bool {
	for _, re := range m.keyREs {
		if re.MatchString(key) {
			return true
		}
	}
	return false
}

// DefaultMaskingPatterns returns a recommended baseline Masking that catches
// common secret patterns in both key names and string values. Profile authors
// can use this as a starting point and extend or replace as needed.
func DefaultMaskingPatterns() Masking {
	return Masking{
		KeyRegex: []string{
			"password",
			"passwd",
			"secret",
			"token",
			`api[_-]?key`,
			`private[_-]?key`,
			`access[_-]?key`,
			`client[_-]?secret`,
			`routing[_-]?key`, // PagerDuty Events v2
			"pagerduty",
			"credential",
			"auth",
		},
		ValueRegex: []string{
			"AKIA[0-9A-Z]{16}",                                   // AWS access key id
			"ASIA[0-9A-Z]{16}",                                   // AWS STS access key
			`eyJ[A-Za-z0-9_=-]{20,}\.`,                           // JWT prefix
			`gh[opsur]_[A-Za-z0-9]{36,}`,                         // GitHub token family (ghp_/gho_/ghu_/ghs_/ghr_)
			`github_pat_[A-Za-z0-9_]{22,}`,                       // GitHub fine-grained PAT
			`glpat-[A-Za-z0-9_-]{20,}`,                           // GitLab PAT
			`sk-[A-Za-z0-9_-]{20,}`,                              // OpenAI/Anthropic keys (sk-ant-…, sk-proj-…)
			"AIza[0-9A-Za-z_-]{35}",                              // Google API key
			`xox[baprse]-[0-9A-Za-z-]{10,}`,                      // Slack bot/user/app tokens
			`xapp-[0-9]-[0-9A-Za-z-]{10,}`,                       // Slack app-level token
			`[0-9]{17,20}\.[A-Za-z0-9_-]{6}\.[A-Za-z0-9_-]{20,}`, // Discord bot token shape
			`[0-9]{6,}:[A-Za-z0-9_-]{20,}`,                       // Telegram bot token shape
			`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`, // full PEM private key block (body included)
			`(?i)bearer\s+[0-9A-Za-z._~+/=-]{10,}`,                                            // generic Bearer auth header
			`(?i)[a-z][a-z0-9+.-]*://[^/\s:@]+:[^/\s@]+@`,                                     // credentials in a URI userinfo (DSNs/conn strings)
		},
	}
}
