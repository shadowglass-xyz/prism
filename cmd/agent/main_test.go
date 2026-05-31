package main_test

import "testing"

func TestSomethingSomething(t *testing.T) {
	a := 1
	b := 2
	if a+b != 3 {
		t.Log("nope nope nope")
		t.Fail()
	}
}
