package registry

import "testing"

type item struct {
	name string
	val  int
}

func nameOf(i item) string { return i.name }

func TestRegisterAndGet(t *testing.T) {
	m := New[item](nameOf)
	if err := m.Register(item{name: "a", val: 1}); err != nil {
		t.Fatalf("register a: %v", err)
	}
	got, ok := m.Get("a")
	if !ok || got.val != 1 {
		t.Fatalf("get a: %+v %v", got, ok)
	}
}

func TestDuplicateRegisterErrors(t *testing.T) {
	m := New[item](nameOf)
	_ = m.Register(item{name: "a"})
	if err := m.Register(item{name: "a"}); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestReplaceOverwrites(t *testing.T) {
	m := New[item](nameOf)
	_ = m.Register(item{name: "a", val: 1})
	m.Replace(item{name: "a", val: 2})
	got, _ := m.Get("a")
	if got.val != 2 {
		t.Fatalf("expected 2, got %d", got.val)
	}
}

func TestAllSortedByName(t *testing.T) {
	m := New[item](nameOf)
	_ = m.Register(item{name: "c"})
	_ = m.Register(item{name: "a"})
	_ = m.Register(item{name: "b"})
	got := m.All()
	if len(got) != 3 || got[0].name != "a" || got[1].name != "b" || got[2].name != "c" {
		t.Fatalf("expected sorted [a b c], got %+v", got)
	}
}

func TestNilNameOfPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	_ = New[item](nil)
}
