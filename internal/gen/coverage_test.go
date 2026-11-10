package gen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTestednessProxy(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 4 exported funcs/methods in source, 2 test funcs → 50%.
	write("svc.go", `package svc

type T struct{}

func Alpha() {}
func Beta() {}
func (t T) Gamma() {}
func Delta() {}

func unexported() {} // not counted (lowercase)
`)
	write("svc_test.go", `package svc

import "testing"

func TestAlpha(t *testing.T) {}
func TestBeta(t *testing.T)  {}
func helper()               {} // not a test entry point
`)

	got := testednessProxy(dir, []string{"svc.go", "svc_test.go"})
	if got != 50 {
		t.Fatalf("testednessProxy = %d, want 50 (2 test funcs / 4 exported)", got)
	}
}

func TestTestednessProxyUntested(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"),
		[]byte("package a\n\nfunc Exported() {}\nfunc AlsoExported() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := testednessProxy(dir, []string{"a.go"})
	if got != 0 {
		t.Fatalf("testednessProxy with no tests = %d, want 0", got)
	}
}

func TestTestednessProxyNoTestableSurface(t *testing.T) {
	// A package of pure types/vars (no exported funcs) has nothing to test
	// and must NOT be flagged as a 0% gap — it returns 100.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "types.go"),
		[]byte("package types\n\ntype User struct{ Name string }\n\nvar Default = User{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := testednessProxy(dir, []string{"types.go"})
	if got != 100 {
		t.Fatalf("testednessProxy with no exported funcs = %d, want 100 (no testable surface)", got)
	}
}

func TestTestednessProxyCapsAt100(t *testing.T) {
	// More test funcs than exported funcs must cap at 100, not exceed it.
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("one.go", "package one\n\nfunc Only() {}\n")
	write("one_test.go", "package one\n\nimport \"testing\"\n\nfunc TestA(t *testing.T){}\nfunc TestB(t *testing.T){}\nfunc TestC(t *testing.T){}\n")
	got := testednessProxy(dir, []string{"one.go", "one_test.go"})
	if got != 100 {
		t.Fatalf("testednessProxy = %d, want 100 (capped)", got)
	}
}
