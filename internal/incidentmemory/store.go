package incidentmemory

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/permission"
)

const (
	dirName  = "incident-memory"
	fileName = "cards.jsonl"
)

var storedIDPattern = regexp.MustCompile(`^(case-\d{8}T\d{6}Z-[0-9a-f]{12}|case-\d{8}T\d{6}\.\d{9}Z|imported-line-\d+)$`)

// Store persists cards as inspectable JSONL under cloudy-owned local state.
type Store struct {
	path   string
	masker *permission.Masker
	cache  string
	now    func() time.Time
}

type cachedCards struct {
	modTime time.Time
	size    int64
	cards   map[string]Card
}

var (
	cardsCacheMu sync.Mutex
	cardsCache   = map[string]cachedCards{}
)

// Path returns the default incident card store path.
func Path() string {
	return filepath.Join(filepath.Dir(config.Path()), dirName, fileName)
}

// NewDefaultStore returns a store using CLOUDY_HOME/config.Path resolution and
// the active profile's masking settings, falling back to default redaction.
func NewDefaultStore() *Store {
	profile, _ := permission.LoadActive()
	return newStore(Path(), permission.MaskerOrDefault(profile), maskingCacheKey(profile))
}

// NewStore builds a store for tests or callers that need an isolated path.
func NewStore(path string, masker *permission.Masker) *Store {
	return newStore(path, masker, fmt.Sprintf("masker:%p", masker))
}

func newStore(path string, masker *permission.Masker, cacheKey string) *Store {
	return &Store{
		path:   path,
		masker: masker,
		cache:  cacheKey,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (s *Store) Path() string { return s.path }

// CreateCandidate validates, redacts, and persists a candidate card. Promotion
// to approved memory remains an explicit HITL action.
func (s *Store) CreateCandidate(c Card) (Card, error) {
	c = normalizeCard(c)
	if err := Validate(c); err != nil {
		return Card{}, fmt.Errorf("incidentmemory: validate: %w", err)
	}
	now := s.now()
	c.ID = makeID(now, c)
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	c.Status = StatusCandidate
	c = s.maskCard(c)

	unlock, err := s.lock()
	if err != nil {
		return Card{}, err
	}
	defer unlock()
	cards, err := s.load()
	if err != nil {
		return Card{}, err
	}
	if _, exists := cards[c.ID]; exists {
		return Card{}, fmt.Errorf("incidentmemory: card %s already exists", c.ID)
	}
	cards[c.ID] = c
	if err := s.rewrite(cards); err != nil {
		return Card{}, err
	}
	return c, nil
}

func (s *Store) Approve(id string) (Card, error) {
	return s.setStatus(id, StatusApproved)
}

func (s *Store) Reject(id string) (Card, error) {
	return s.setStatus(id, StatusRejected)
}

func (s *Store) Get(id string) (Card, error) {
	cards, err := s.load()
	if err != nil {
		return Card{}, err
	}
	c, ok := cards[id]
	if !ok {
		return Card{}, fmt.Errorf("incidentmemory: unknown card id %s", id)
	}
	return c, nil
}

// List returns cards sorted by creation time, newest first.
func (s *Store) List() ([]Card, error) {
	cards, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]Card, 0, len(cards))
	for _, c := range cards {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *Store) setStatus(id, status string) (Card, error) {
	if err := validateStatus(status); err != nil {
		return Card{}, err
	}
	unlock, err := s.lock()
	if err != nil {
		return Card{}, err
	}
	defer unlock()
	cards, err := s.load()
	if err != nil {
		return Card{}, err
	}
	c, ok := cards[id]
	if !ok {
		return Card{}, fmt.Errorf("incidentmemory: unknown card id %s", id)
	}
	c.Status = status
	c.UpdatedAt = s.now()
	cards[id] = c
	if err := s.rewrite(cards); err != nil {
		return Card{}, err
	}
	return c, nil
}

func (s *Store) load() (map[string]Card, error) {
	out := map[string]Card{}
	info, err := os.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("incidentmemory: stat %s: %w", s.path, err)
	}
	if cached, ok := s.cachedCards(info); ok {
		return cached, nil
	}

	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("incidentmemory: open %s: %w", s.path, err)
	}
	defer f.Close()

	const maxLine = 1024 * 1024
	reader := bufio.NewReader(f)
	lineNo := 0
	for {
		line, oversized, readErr := readBoundedLine(reader, maxLine)
		if readErr != nil && readErr != io.EOF {
			return nil, fmt.Errorf("incidentmemory: read %s: %w", s.path, readErr)
		}
		if readErr == io.EOF && len(line) == 0 {
			break
		}
		lineNo++
		if oversized {
			if readErr == io.EOF {
				break
			}
			continue
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			if readErr == io.EOF {
				break
			}
			continue
		}
		var c Card
		if err := json.Unmarshal(line, &c); err != nil {
			if readErr == io.EOF {
				break
			}
			continue
		}
		var ok bool
		c, ok = s.prepareLoadedCard(c, lineNo)
		if ok {
			out[c.ID] = c
		}
		if readErr == io.EOF {
			break
		}
	}
	if after, err := os.Stat(s.path); err == nil && sameFileState(info, after) {
		s.storeCachedCards(after, out)
	}
	return out, nil
}

func (s *Store) cachedCards(info os.FileInfo) (map[string]Card, bool) {
	cardsCacheMu.Lock()
	defer cardsCacheMu.Unlock()
	cached, ok := cardsCache[s.cacheKey()]
	if !ok || cached.size != info.Size() || !cached.modTime.Equal(info.ModTime()) {
		return nil, false
	}
	return cloneCardMap(cached.cards), true
}

func (s *Store) storeCachedCards(info os.FileInfo, cards map[string]Card) {
	cardsCacheMu.Lock()
	defer cardsCacheMu.Unlock()
	cardsCache[s.cacheKey()] = cachedCards{
		modTime: info.ModTime(),
		size:    info.Size(),
		cards:   cloneCardMap(cards),
	}
}

func (s *Store) cacheKey() string {
	return s.path + "\x00" + s.cache
}

func sameFileState(a, b os.FileInfo) bool {
	return a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func readBoundedLine(reader *bufio.Reader, maxBytes int) ([]byte, bool, error) {
	var line []byte
	oversized := false
	for {
		part, err := reader.ReadSlice('\n')
		if len(part) > 0 {
			if !oversized && len(line)+len(part) <= maxBytes {
				line = append(line, part...)
			} else {
				oversized = true
				line = nil
			}
		}
		switch err {
		case nil:
			return line, oversized, nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			return line, oversized, io.EOF
		default:
			return nil, oversized, err
		}
	}
}

func (s *Store) prepareLoadedCard(c Card, lineNo int) (Card, bool) {
	c = normalizeCard(c)
	c.ID = cleanStoredID(c.ID, lineNo)
	if err := validateStatus(c.Status); err != nil {
		return Card{}, false
	}
	if err := Validate(c); err != nil {
		return Card{}, false
	}
	c = s.maskCard(c)
	return c, true
}

func cleanStoredID(id string, lineNo int) string {
	id = cleanText(id)
	if storedIDPattern.MatchString(id) {
		return id
	}
	return fmt.Sprintf("imported-line-%d", lineNo)
}

func (s *Store) rewrite(cards map[string]Card) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("incidentmemory: mkdir: %w", err)
	}
	if err := os.Chmod(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("incidentmemory: chmod dir: %w", err)
	}

	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("incidentmemory: open temp %s: %w", tmp, err)
	}
	enc := json.NewEncoder(f)
	list := make([]Card, 0, len(cards))
	for _, c := range cards {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	for _, c := range list {
		if err := enc.Encode(c); err != nil {
			_ = f.Close()
			return fmt.Errorf("incidentmemory: encode: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("incidentmemory: close temp: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return fmt.Errorf("incidentmemory: chmod file: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("incidentmemory: rename: %w", err)
	}
	if info, err := os.Stat(s.path); err == nil {
		s.storeCachedCards(info, cards)
	}
	return nil
}

func (s *Store) lock() (func(), error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return nil, fmt.Errorf("incidentmemory: mkdir lock dir: %w", err)
	}
	lockPath := filepath.Join(filepath.Dir(s.path), "cards.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("incidentmemory: open lock %s: %w", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("incidentmemory: lock %s: %w", lockPath, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

func (s *Store) maskCard(c Card) Card {
	mask := func(v string) string { return maskText(s.masker, v) }
	for i := range c.Symptoms {
		c.Symptoms[i] = mask(c.Symptoms[i])
	}
	c.AffectedService = mask(c.AffectedService)
	for i := range c.Signals {
		c.Signals[i] = mask(c.Signals[i])
	}
	c.Cause = mask(c.Cause)
	c.FixOrMitigation = mask(c.FixOrMitigation)
	c.WhatWasDifferent = mask(c.WhatWasDifferent)
	c.Source.Type = mask(c.Source.Type)
	c.Source.ID = mask(c.Source.ID)
	c.Source.Path = mask(c.Source.Path)
	c.Source.Ref = mask(c.Source.Ref)
	for i := range c.Tags {
		c.Tags[i] = mask(c.Tags[i])
	}
	return c
}

func maskText(masker *permission.Masker, v string) string {
	if masked, err := masker.MaskJSON([]byte(v)); err == nil {
		v = string(masked)
	}
	return masker.MaskString(v)
}

func maskingCacheKey(profile *permission.Profile) string {
	masking := permission.DefaultMaskingPatterns()
	if m, err := permission.NewMasker(profile); err == nil && m != nil {
		masking = profile.Masking
	}
	data, _ := json.Marshal(masking)
	sum := sha256.Sum256(data)
	return "masking:" + hex.EncodeToString(sum[:])
}

func cloneCardMap(in map[string]Card) map[string]Card {
	out := make(map[string]Card, len(in))
	for id, c := range in {
		out[id] = cloneCard(c)
	}
	return out
}

func cloneCard(c Card) Card {
	c.Symptoms = append([]string(nil), c.Symptoms...)
	c.Signals = append([]string(nil), c.Signals...)
	c.Tags = append([]string(nil), c.Tags...)
	return c
}

func makeID(now time.Time, _ Card) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is vanishingly rare; keep the id opaque even in
		// that case by using timestamp-only entropy instead of card content.
		return "case-" + now.Format("20060102T150405.000000000Z")
	}
	return "case-" + now.Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}
