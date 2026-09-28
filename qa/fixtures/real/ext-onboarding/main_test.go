package main

import "testing"

func TestGreetingDefault(t *testing.T) {
	if got := greeting("world"); got != "Hello, world!" {
		t.Fatalf("got %q", got)
	}
}

func TestGreetingName(t *testing.T) {
	if got := greeting("kiln"); got != "Hello, kiln!" {
		t.Fatalf("got %q", got)
	}
}
