package context

import (
	"testing"

	"github.com/distribution/distribution/v3/internal/dcontext"
	"github.com/sirupsen/logrus"
)

// TestLoggerSharedWithInternal verifies that a logger installed through
// internal/dcontext, as the registry handlers do, is visible through this
// package. This is the reason the package forwards instead of copying.
func TestLoggerSharedWithInternal(t *testing.T) {
	entry := logrus.New().WithField("shared", "yes")
	ctx := dcontext.WithLogger(Background(), entry)

	got, ok := GetLogger(ctx).(*logrus.Entry)
	if !ok {
		t.Fatalf("GetLogger returned %T, want *logrus.Entry", GetLogger(ctx))
	}
	if got.Data["shared"] != "yes" {
		t.Fatalf("logger field lost across packages: %v", got.Data)
	}

	ctx = WithLogger(Background(), entry)
	if dcontext.GetLogger(ctx).(*logrus.Entry).Data["shared"] != "yes" {
		t.Fatal("logger installed through context is not visible to internal/dcontext")
	}
}
