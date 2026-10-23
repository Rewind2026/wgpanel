package ipam

import (
	"net/netip"
	"testing"
)

func TestAllocatorSkipsNetworkAndTakenAddresses(t *testing.T) {
	used := map[string]struct{}{
		"10.8.0.1": {}, // 服务端自身
		"10.8.0.2": {}, // 已被占用
	}
	alloc, err := NewAllocator(netip.MustParsePrefix("10.8.0.1/24"), used)
	if err != nil {
		t.Fatalf("NewAllocator: %v", err)
	}

	got, err := alloc.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got != "10.8.0.3/32" {
		t.Fatalf("Next = %q, want 10.8.0.3/32", got)
	}
}

func TestAllocatorHandsOutUniqueAddresses(t *testing.T) {
	alloc, err := NewAllocator(netip.MustParsePrefix("10.8.0.1/29"), nil)
	if err != nil {
		t.Fatalf("NewAllocator: %v", err)
	}

	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		addr, err := alloc.Next()
		if err != nil {
			t.Fatalf("Next #%d: %v", i, err)
		}
		if seen[addr] {
			t.Fatalf("duplicate address %q", addr)
		}
		seen[addr] = true
	}
	if _, err := alloc.Next(); err == nil {
		t.Fatal("expected pool exhaustion on a /29")
	}
}

func TestAllocatorReleaseFreesAddress(t *testing.T) {
	alloc, err := NewAllocator(netip.MustParsePrefix("10.8.0.1/29"), nil)
	if err != nil {
		t.Fatalf("NewAllocator: %v", err)
	}
	first, _ := alloc.Next()

	alloc.Release(first)
	next, err := alloc.Next()
	if err != nil {
		t.Fatalf("Next after Release: %v", err)
	}
	if HostAddress(next) != HostAddress(first) {
		t.Fatalf("released address %q was not reused, got %q", first, next)
	}
}

func TestNewAllocatorRejectsUnsupportedPrefix(t *testing.T) {
	cases := []string{"10.8.0.1/8", "10.8.0.1/32", "fd00::1/64"}
	for _, c := range cases {
		if _, err := NewAllocator(netip.MustParsePrefix(c), nil); err == nil {
			t.Fatalf("prefix %q should be rejected", c)
		}
	}
}

func TestInPrefix(t *testing.T) {
	if !InPrefix("10.8.0.7/32", "10.8.0.0/24") {
		t.Fatal("10.8.0.7 should be inside 10.8.0.0/24")
	}
	if InPrefix("10.9.0.7/32", "10.8.0.0/24") {
		t.Fatal("10.9.0.7 should be outside 10.8.0.0/24")
	}
}

func TestNormalizeAddressesDeduplicates(t *testing.T) {
	got := NormalizeAddresses([]string{"10.8.0.3/32", "10.8.0.2", "10.8.0.3/32"})
	want := []string{"10.8.0.2", "10.8.0.3"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
