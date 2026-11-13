package main

import (
	"testing"

	"github.com/scaleway/scaleway-sdk-go/api/instance/v1"
)

func TestIsAvailable_MissingTreatedAvailable(t *testing.T) {
	if !isAvailable(map[string]*instance.GetServerTypesAvailabilityResponseAvailability{}, "POP2-2C-8G") {
		t.Fatal("missing entry should default to available")
	}
}

func TestIsAvailable_NilEntryTreatedAvailable(t *testing.T) {
	m := map[string]*instance.GetServerTypesAvailabilityResponseAvailability{
		"POP2-2C-8G": nil,
	}
	if !isAvailable(m, "POP2-2C-8G") {
		t.Fatal("nil entry should default to available")
	}
}

func TestIsAvailable_ExplicitAvailable(t *testing.T) {
	m := map[string]*instance.GetServerTypesAvailabilityResponseAvailability{
		"POP2-2C-8G": {Availability: instance.ServerTypesAvailabilityAvailable},
	}
	if !isAvailable(m, "POP2-2C-8G") {
		t.Fatal("ServerTypesAvailabilityAvailable → true")
	}
}

func TestIsAvailable_ScarceStillAvailable(t *testing.T) {
	// Scarce = limited stock but creation still succeeds. Karpenter's
	// Available is binary, so we don't pre-empt the API: only an explicit
	// shortage flips us to false.
	m := map[string]*instance.GetServerTypesAvailabilityResponseAvailability{
		"POP2-2C-8G": {Availability: instance.ServerTypesAvailabilityScarce},
	}
	if !isAvailable(m, "POP2-2C-8G") {
		t.Fatal("scarce should remain available")
	}
}

func TestIsAvailable_ShortageFalse(t *testing.T) {
	m := map[string]*instance.GetServerTypesAvailabilityResponseAvailability{
		"POP2-2C-8G": {Availability: instance.ServerTypesAvailabilityShortage},
	}
	if isAvailable(m, "POP2-2C-8G") {
		t.Fatal("shortage should flip to false")
	}
}
