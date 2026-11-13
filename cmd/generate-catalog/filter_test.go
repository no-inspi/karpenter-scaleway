package main

import (
	"testing"

	"github.com/scaleway/scaleway-sdk-go/api/instance/v1"
)

func u64(v uint64) *uint64 { return &v }

func TestAccept_NormalAmd64(t *testing.T) {
	st := &instance.ServerType{Arch: instance.ArchX86_64, EndOfService: false}
	if !accept("POP2-2C-8G", st).keep {
		t.Fatal("expected accept")
	}
}

func TestAccept_RejectsEndOfService(t *testing.T) {
	st := &instance.ServerType{Arch: instance.ArchX86_64, EndOfService: true}
	r := accept("DEV1-S", st)
	if r.keep || r.reason != "endOfService" {
		t.Fatalf("got %+v", r)
	}
}

func TestAccept_RejectsGPU(t *testing.T) {
	st := &instance.ServerType{Arch: instance.ArchX86_64, Gpu: u64(2)}
	r := accept("H100-1-80G", st)
	if r.keep || r.reason != "gpu" {
		t.Fatalf("got %+v", r)
	}
}

func TestAccept_RejectsArm32(t *testing.T) {
	st := &instance.ServerType{Arch: instance.ArchArm}
	r := accept("LEGACY-ARM", st)
	if r.keep || r.reason != "arch" {
		t.Fatalf("got %+v", r)
	}
}

func TestAccept_AcceptsArm64(t *testing.T) {
	st := &instance.ServerType{Arch: instance.ArchArm64}
	if !accept("COPARM1-2C-8G", st).keep {
		t.Fatal("expected accept")
	}
}

func TestAccept_RejectsStardust(t *testing.T) {
	st := &instance.ServerType{Arch: instance.ArchX86_64}
	r := accept("STARDUST", st)
	if r.keep || r.reason != "namePrefix" {
		t.Fatalf("got %+v", r)
	}
}
