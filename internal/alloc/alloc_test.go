package alloc

import (
	"path/filepath"
	"testing"
)

func TestPortStable(t *testing.T) {
	a := New(18081)
	p1, err := a.Port("1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := a.Port("1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Fatalf("same IP should get same port: %d vs %d", p1, p2)
	}
	if p1 != 18081 {
		t.Fatalf("expected 18081, got %d", p1)
	}
}

func TestDifferentIPsDifferentPorts(t *testing.T) {
	a := New(18081)
	ips := []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"}
	ports := make(map[int]bool)
	for _, ip := range ips {
		p, err := a.Port(ip)
		if err != nil {
			t.Fatal(err)
		}
		if ports[p] {
			t.Fatalf("duplicate port %d", p)
		}
		ports[p] = true
	}
	if len(ports) != 3 {
		t.Fatalf("expected 3 distinct ports, got %d", len(ports))
	}
}

func TestSaveLoadPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	a1 := New(18081)
	a1.Port("1.2.3.4")
	a1.Port("5.6.7.8")
	if err := a1.Save(path); err != nil {
		t.Fatal(err)
	}

	a2 := New(18081)
	if err := a2.Load(path); err != nil {
		t.Fatal(err)
	}
	p1, _ := a2.Port("1.2.3.4")
	p2, _ := a2.Port("5.6.7.8")
	if p1 != 18081 || p2 != 18082 {
		t.Fatalf("expected 18081/18082, got %d/%d", p1, p2)
	}
}

func TestLoadMissingFile(t *testing.T) {
	a := New(18081)
	err := a.Load("/nonexistent/state.json")
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	p, _ := a.Port("1.1.1.1")
	if p != 18081 {
		t.Fatalf("expected 18081, got %d", p)
	}
}

func TestSaveUnwritable(t *testing.T) {
	a := New(18081)
	a.Port("1.1.1.1")
	err := a.Save("/nonexistent/dir/sub/state.json")
	if err == nil {
		t.Fatal("expected error for unwritable path")
	}
}

func TestPortExhaustion(t *testing.T) {
	a := New(65535)
	_, err := a.Port("1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Port("2.2.2.2")
	if err == nil {
		t.Fatal("expected port exhaustion error")
	}
}
