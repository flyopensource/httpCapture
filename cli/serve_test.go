package main

import (
	"strings"
	"testing"
)

func TestResolveServeControlHostDisabledByDefault(t *testing.T) {
	host, enabled, err := resolveServeControlHostWithDiscover("", false, func() string {
		t.Fatal("discover should not be called when control is disabled")
		return ""
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if enabled {
		t.Fatal("control should be disabled without --pair or --control-host")
	}
	if host != "" {
		t.Fatalf("unexpected host: %q", host)
	}
}

func TestResolveServeControlHostUsesExplicitHost(t *testing.T) {
	host, enabled, err := resolveServeControlHostWithDiscover(" 192.168.1.10 ", false, func() string {
		t.Fatal("discover should not be called for explicit host")
		return ""
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !enabled {
		t.Fatal("control should be enabled for explicit host")
	}
	if host != "192.168.1.10" {
		t.Fatalf("host = %q", host)
	}
}

func TestResolveServeControlHostAutoDetectsForPair(t *testing.T) {
	host, enabled, err := resolveServeControlHostWithDiscover("", true, func() string {
		return "192.168.1.23"
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !enabled {
		t.Fatal("control should be enabled for --pair")
	}
	if host != "192.168.1.23" {
		t.Fatalf("host = %q", host)
	}
}

func TestResolveServeControlHostRequiresOverrideWhenAutoDetectFails(t *testing.T) {
	_, enabled, err := resolveServeControlHostWithDiscover("", true, func() string {
		return ""
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if enabled {
		t.Fatal("control should not be enabled")
	}
	if !strings.Contains(err.Error(), "--control-host") {
		t.Fatalf("error should mention --control-host: %v", err)
	}
}
