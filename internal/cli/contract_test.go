//go:build linux

package cli_test

import (
	"errors"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/email"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/cli"
	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
)

// TestAdaptersShareOutcomes: each adapter's exit errors are the shared ones, so an exit code's
// ExitError from either adapter matches the other adapter's error of the same code, and no
// other.
func TestAdaptersShareOutcomes(t *testing.T) {
	officeErrs := []error{office.ErrMediaNotWritten, office.ErrUsage, office.ErrRefused, office.ErrFailed}
	emailErrs := []error{email.ErrNotWritten, email.ErrUsage, email.ErrRefused, email.ErrFailed}
	for i := range officeErrs {
		code := i + 1
		if officeErrs[i] != cli.Outcome(code) || emailErrs[i] != cli.Outcome(code) {
			t.Errorf("code %d: office's %v and email's %v aren't the shared %v", code, officeErrs[i], emailErrs[i], cli.Outcome(code))
		}
		officeErr := error(&office.ExitError{Code: code, Message: "m"})
		emailErr := error(&email.ExitError{Code: code, Message: "m"})
		for j := range emailErrs {
			if errors.Is(officeErr, emailErrs[j]) != (i == j) {
				t.Errorf("errors.Is(office code %d, %v) = %t", code, emailErrs[j], i != j)
			}
			if errors.Is(emailErr, officeErrs[j]) != (i == j) {
				t.Errorf("errors.Is(email code %d, %v) = %t", code, officeErrs[j], i != j)
			}
		}
	}
	if !errors.Is(&office.ExitError{Code: 3}, email.ErrRefused) || !errors.Is(&email.ExitError{Code: 3}, office.ErrRefused) {
		t.Error("a refusal doesn't match across the adapters")
	}
}
