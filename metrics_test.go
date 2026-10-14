package metrics

import (
	"testing"
)

func TestInit_OnceOnly(t *testing.T) {
	Init("test-service")
	Init("test-service") // should not panic
}

func TestRegistry_NotNil(t *testing.T) {
	if Registry == nil {
		t.Error("expected non-nil Registry")
	}
}

func TestBuildInfo_NotNil(t *testing.T) {
	if BuildInfo == nil {
		t.Error("expected non-nil BuildInfo")
	}
}

func TestStartTimeSeconds_NotNil(t *testing.T) {
	if StartTimeSeconds == nil {
		t.Error("expected non-nil StartTimeSeconds")
	}
}
