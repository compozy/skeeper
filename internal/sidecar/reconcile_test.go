package sidecar

import "testing"

func TestValidateHydrateOptionsRejectsAmbiguousMergePolicies(t *testing.T) {
	t.Parallel()

	for name, opts := range map[string]HydrateOptions{
		"merge ours":   {Merge: true, Ours: true},
		"merge theirs": {Merge: true, Theirs: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := validateHydrateOptions(opts); err == nil {
				t.Fatalf("expected %s to be rejected", name)
			}
		})
	}
}
