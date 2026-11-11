package common

import (
	"slices"
	"strings"
)

// DevinModels is the complete client-facing catalog supported by the Devin
// subscription relay. Bare family names resolve to their high-effort model.
func DevinModels() []string {
	return []string{
		"swe-2",
		"swe-2-low",
		"swe-2-medium",
		"swe-2-high",
		"swe-2-max",
		"swe-2-low-usa",
		"swe-2-high-usa",
		"swe-2-max-usa",
		"swe-2-medium-lightning",
		"swe-2-high-lightning",
		"swe-2-max-lightning",
		"swe-2-flash",
		"swe-2-flash-low",
		"swe-2-flash-medium",
		"swe-2-flash-high",
		"swe-2-flash-max",
	}
}

// ResolveDevinModelUID rejects names outside the exact supported catalog.
func ResolveDevinModelUID(model string) (string, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	if !slices.Contains(DevinModels(), model) {
		return "", false
	}
	switch model {
	case "swe-2":
		return "swe-2-high", true
	case "swe-2-flash":
		return "swe-2-flash-high", true
	default:
		return model, true
	}
}
