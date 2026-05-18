package dispatch

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// TestPBT_BucketByModelTier_Purity — same input → same output, every
// call. Core purity property. Bucket functions are used in synthesis
// + diversity scoring; a non-pure function would corrupt the panel-
// rotation history that ADR-0001 relies on.
func TestPBT_BucketByModelTier_Purity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Build a random PanelMember. Model is the only field that
		// affects BucketByModelTier — generate it broadly to cover
		// the tier substrings + the "other" fallback.
		model := rapid.StringMatching("[a-zA-Z0-9.:_-]{0,40}").Draw(t, "model")
		m := PanelMember{Model: model}
		a := BucketByModelTier(m)
		b := BucketByModelTier(m)
		if a != b {
			t.Fatalf("non-pure: model=%q → %q then %q", model, a, b)
		}
		// And the returned bucket should be from the known label set
		// (never empty, no spaces, lowercase tokens).
		if a == "" {
			t.Fatalf("model=%q → empty bucket", model)
		}
		if strings.ContainsRune(a, ' ') {
			t.Fatalf("model=%q → bucket with space %q", model, a)
		}
	})
}

// TestPBT_BucketByModelTier_KnownTiers — for any model id whose lower-
// cased form contains "opus", BucketByModelTier returns "opus";
// similarly "sonnet" → "sonnet", "haiku" → "haiku". This pins the
// invariant the v0.4.0 panel reshape depends on: intra-Claude diversity
// scoring assumes the three tiers each occupy their own bucket.
func TestPBT_BucketByModelTier_KnownTiers(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		tier := rapid.SampledFrom([]string{"opus", "sonnet", "haiku"}).Draw(t, "tier")
		// Construct a model id with the tier substring embedded in
		// random positions surrounded by random prefix/suffix.
		prefix := rapid.StringMatching("[a-z-]{0,12}").Draw(t, "prefix")
		suffix := rapid.StringMatching("[a-z0-9.-]{0,12}").Draw(t, "suffix")
		model := prefix + tier + suffix
		m := PanelMember{Model: model}
		got := BucketByModelTier(m)
		if got != tier {
			t.Fatalf("model=%q (contains %q) → bucket=%q want %q", model, tier, got, tier)
		}
	})
}

// TestPBT_BucketByVendorFamily_FixedMap — vendor family is keyed off
// Provider only; the mapping is constant. claude → anthropic; openai
// → openai; google → google; local → local; everything else → other.
// Property pins the v0.4.0 reshape's assumption that a "claude" provider
// always lands in the anthropic bucket regardless of model.
func TestPBT_BucketByVendorFamily_FixedMap(t *testing.T) {
	known := map[string]string{
		"claude": "anthropic",
		"openai": "openai",
		"google": "google",
		"local":  "local",
	}
	rapid.Check(t, func(t *rapid.T) {
		provider := rapid.StringMatching("[A-Za-z0-9_-]{0,20}").Draw(t, "provider")
		m := PanelMember{Provider: provider}
		got := BucketByVendorFamily(m)
		want, ok := known[strings.ToLower(provider)]
		if !ok {
			want = "other"
		}
		if got != want {
			t.Fatalf("provider=%q → %q want %q", provider, got, want)
		}
	})
}
