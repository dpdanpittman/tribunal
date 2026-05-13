package dispatch

import (
	"fmt"
	"strings"
)

// BucketFn maps a PanelMember to a diversity-bucket label. Findings whose
// member buckets match are considered same-bucket for the reputation
// system's diversity bonus.
type BucketFn func(m PanelMember) string

// BucketByVendorFamily groups by upstream vendor family. Most theoretically
// strong axis but most expensive to achieve (requires multi-vendor
// dispatch).
func BucketByVendorFamily(m PanelMember) string {
	switch strings.ToLower(m.Provider) {
	case "claude":
		return "anthropic"
	case "openai":
		return "openai"
	case "google":
		return "google"
	case "local":
		return "local"
	default:
		return "other"
	}
}

// BucketByTemperatureBand groups by temperature band:
//   - deterministic: t ≤ 0.2
//   - balanced: 0.2 < t ≤ 0.6
//   - creative: t > 0.6
func BucketByTemperatureBand(m PanelMember) string {
	switch {
	case m.Temperature <= 0.2:
		return "deterministic"
	case m.Temperature <= 0.6:
		return "balanced"
	default:
		return "creative"
	}
}

// BucketByFocus groups by the member's configured prompt focus.
func BucketByFocus(m PanelMember) string {
	if m.Focus == "" {
		return "general"
	}
	return strings.ToLower(m.Focus)
}

// BucketByModelTier groups by a coarse cost/capability tier inferred from
// the model id. Useful for catching findings unique to a tier (e.g. only
// opus-class models catch this; only haiku/sonnet-class miss it).
func BucketByModelTier(m PanelMember) string {
	id := strings.ToLower(m.Model)
	switch {
	case strings.Contains(id, "opus"):
		return "opus"
	case strings.Contains(id, "sonnet"):
		return "sonnet"
	case strings.Contains(id, "haiku"):
		return "haiku"
	case strings.HasPrefix(id, "gpt-5"), strings.HasPrefix(id, "o5"):
		return "gpt-5-class"
	case strings.HasPrefix(id, "gpt-4"), strings.HasPrefix(id, "o3"), strings.HasPrefix(id, "o4"):
		return "gpt-4-class"
	case strings.HasPrefix(id, "gemini-2.5"):
		return "gemini-2.5"
	case strings.HasPrefix(id, "gemini"):
		return "gemini"
	case strings.Contains(id, "32b"), strings.Contains(id, "70b"), strings.Contains(id, "72b"), strings.Contains(id, "110b"):
		return "local-large"
	case strings.Contains(id, "7b"), strings.Contains(id, "8b"), strings.Contains(id, "13b"):
		return "local-medium"
	case strings.Contains(id, "3b"), strings.Contains(id, "1.5b"):
		return "local-small"
	default:
		return "other"
	}
}

// BucketComposite returns the concatenation of N other bucket functions'
// outputs, joined by "+".
func BucketComposite(fns ...BucketFn) BucketFn {
	return func(m PanelMember) string {
		parts := make([]string, len(fns))
		for i, fn := range fns {
			parts[i] = fn(m)
		}
		return strings.Join(parts, "+")
	}
}

// SelectBucket returns the BucketFn matching the given config string.
// Known values: "vendor_family", "temperature_band", "focus", "model_tier",
// and "composite:axis1,axis2,...".
//
// SelectBucket("") and SelectBucket("default") return
// BucketComposite(BucketByVendorFamily, BucketByFocus).
func SelectBucket(spec string) (BucketFn, error) {
	if spec == "" || spec == "default" {
		return BucketComposite(BucketByVendorFamily, BucketByFocus), nil
	}
	if strings.HasPrefix(spec, "composite:") {
		axes := strings.Split(strings.TrimPrefix(spec, "composite:"), ",")
		fns := make([]BucketFn, 0, len(axes))
		for _, a := range axes {
			fn, err := atomicBucketFn(strings.TrimSpace(a))
			if err != nil {
				return nil, err
			}
			fns = append(fns, fn)
		}
		return BucketComposite(fns...), nil
	}
	return atomicBucketFn(spec)
}

func atomicBucketFn(name string) (BucketFn, error) {
	switch name {
	case "vendor_family":
		return BucketByVendorFamily, nil
	case "temperature_band":
		return BucketByTemperatureBand, nil
	case "focus":
		return BucketByFocus, nil
	case "model_tier":
		return BucketByModelTier, nil
	default:
		return nil, fmt.Errorf("dispatch: unknown bucket %q (known: vendor_family, temperature_band, focus, model_tier, composite:axis1,axis2,...)", name)
	}
}
