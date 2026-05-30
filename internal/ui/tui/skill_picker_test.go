package tui

import (
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/core/skills"
)

// makeSkillsRegistry constructs a minimal *skills.Registry for picker tests.
// The skills are built from raw markdown so they pass the parser's validation.
func makeSkillsRegistry(names ...string) *skills.Registry {
	var ss []*skills.Skill
	for _, n := range names {
		ss = append(ss, &skills.Skill{
			Name:         n,
			Description:  "description of " + n,
			AllowedTools: []string{"k8s_list_nodes"},
		})
	}
	return skills.New(ss)
}

// TestSkillPicker_BareSlashOpensPicker confirms that `/skill` with no arg
// opens the interactive picker instead of emitting the old usage message.
func TestSkillPicker_BareSlashOpensPicker(t *testing.T) {
	deps := makeDeps()
	deps.Skills = makeSkillsRegistry("k8s-incident", "cost-analysis")
	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	_ = m.handlePaletteAction(paletteActionMsg{cmd: "skill", arg: ""})

	if !m.skillPickerActive {
		t.Error("/skill (no arg) must set skillPickerActive")
	}
	if m.arrowPicker == nil {
		t.Fatal("/skill (no arg) must install the picker")
	}
	if m.arrowPicker.multiSelect {
		t.Error("skill picker must be single-select")
	}
	if len(m.arrowPicker.items) != 2 {
		t.Fatalf("skill picker should have 2 rows (one per skill), got %d", len(m.arrowPicker.items))
	}
}

// TestSkillPicker_ResolveActivatesSkill confirms that picker confirmation
// routes through the identical `/skill <name>` activation path: activeSkill
// is set and the header receives the skill name.
func TestSkillPicker_ResolveActivatesSkill(t *testing.T) {
	deps := makeDeps()
	deps.Skills = makeSkillsRegistry("k8s-incident", "cost-analysis")
	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	// Open the picker.
	_ = m.handlePaletteAction(paletteActionMsg{cmd: "skill", arg: ""})

	// Simulate the picker firing its resolve message with the user's pick.
	next, _ = m.Update(arrowPickerResolveMsg{key: "k8s-incident"})
	m = next.(Model)

	if m.activeSkill != "k8s-incident" {
		t.Errorf("activeSkill = %q, want k8s-incident", m.activeSkill)
	}
	if m.skillPickerActive {
		t.Error("skillPickerActive must reset after resolve")
	}
	if m.arrowPicker != nil {
		t.Error("arrowPicker must clear after resolve")
	}
}

// TestSkillPicker_CancelDoesNotActivate — Esc on the picker must NOT
// activate any skill and must NOT touch activeSkill. Without this guard,
// hitting Esc would silently route through skill activation with an empty
// name, which would emit "skill not found".
func TestSkillPicker_CancelDoesNotActivate(t *testing.T) {
	deps := makeDeps()
	deps.Skills = makeSkillsRegistry("k8s-incident")
	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	_ = m.handlePaletteAction(paletteActionMsg{cmd: "skill", arg: ""})
	prev := m.activeSkill

	next, outCmd := m.Update(arrowPickerResolveMsg{cancelled: true})
	m = next.(Model)

	if m.activeSkill != prev {
		t.Errorf("activeSkill changed on cancel: got %q, want %q", m.activeSkill, prev)
	}
	if m.skillPickerActive {
		t.Error("skillPickerActive must reset on cancel")
	}
	if !strings.Contains(printedText(outCmd), "cancelled") {
		t.Errorf("scrollback should announce the cancel, got: %q", printedText(outCmd))
	}
}

// TestSkillPicker_ExplicitNameStillWorks — `/skill k8s-incident` (with an
// explicit name) bypasses the picker and activates directly. This is the
// power-user / scripted path; the picker is for arrow-key users.
func TestSkillPicker_ExplicitNameStillWorks(t *testing.T) {
	deps := makeDeps()
	deps.Skills = makeSkillsRegistry("k8s-incident")
	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	_ = m.handlePaletteAction(paletteActionMsg{cmd: "skill", arg: "k8s-incident"})

	if m.activeSkill != "k8s-incident" {
		t.Errorf("explicit /skill <name> should activate directly, got %q", m.activeSkill)
	}
	if m.skillPickerActive {
		t.Error("explicit name must not open the picker")
	}
	if m.arrowPicker != nil {
		t.Error("explicit name must not leave a picker installed")
	}
}

// TestBuildSkillsPicker_RowsMatchRegistry locks in that buildSkillsPicker
// produces one row per skill in alphabetical order with correct key/hint.
func TestBuildSkillsPicker_RowsMatchRegistry(t *testing.T) {
	reg := makeSkillsRegistry("zebra-skill", "alpha-skill")
	picker := buildSkillsPicker(reg)

	if picker == nil {
		t.Fatal("buildSkillsPicker returned nil for a non-empty registry")
	}
	// skills.Registry.List() returns alphabetical order.
	if len(picker.items) != 2 {
		t.Fatalf("picker has %d rows, want 2", len(picker.items))
	}
	if picker.items[0].key != "alpha-skill" {
		t.Errorf("first row should be alpha-skill (alphabetical), got %q", picker.items[0].key)
	}
	if picker.items[1].key != "zebra-skill" {
		t.Errorf("second row should be zebra-skill, got %q", picker.items[1].key)
	}
	for _, it := range picker.items {
		if it.hint == "" {
			t.Errorf("row %q has no hint — description should appear as the hint", it.key)
		}
	}
}

// TestBuildSkillsPicker_EmptyRegistryReturnsNil confirms that an empty
// registry produces nil (no picker to open).
func TestBuildSkillsPicker_EmptyRegistryReturnsNil(t *testing.T) {
	reg := makeSkillsRegistry()
	if buildSkillsPicker(reg) != nil {
		t.Error("buildSkillsPicker should return nil for an empty registry")
	}
}

// TestSkillPicker_NilSkillsRegistry confirms that bare `/skill` with a nil
// Skills registry writes an informative message instead of panicking.
func TestSkillPicker_NilSkillsRegistry(t *testing.T) {
	deps := makeDeps()
	// deps.Skills deliberately left nil
	m := NewModel(deps)
	next, _ := m.Update(windowMsg())
	m = next.(Model)

	cmd := m.handlePaletteAction(paletteActionMsg{cmd: "skill", arg: ""})

	if m.skillPickerActive {
		t.Error("skillPickerActive must not be set when Skills is nil")
	}
	if m.arrowPicker != nil {
		t.Error("arrowPicker must not be installed when Skills is nil")
	}
	if !strings.Contains(printedText(cmd), "no skills") {
		t.Errorf("should emit a 'no skills' message, got: %q", printedText(cmd))
	}
}
