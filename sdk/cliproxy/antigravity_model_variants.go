package cliproxy

import (
	"slices"
	"sort"
	"strings"
)

// appendAntigravityEffortVariants exposes account-reported reasoning variants
// of known model families. Unknown families and unreported variants stay hidden.
// Family metadata comes from the maintained catalog, not unvalidated network data.
func appendAntigravityEffortVariants(filtered, catalog []*ModelInfo, hints antigravityModelCapabilityHints) []*ModelInfo {
	templates := make(map[string]*ModelInfo)
	for _, model := range catalog {
		if model == nil || model.Thinking == nil || len(model.Thinking.Levels) == 0 {
			continue
		}
		id := normalizeAntigravityFetchedModelID(model.ID)
		family, _ := antigravityEffortVariant(id)
		if family == "" {
			family = id
		}
		if templates[family] == nil {
			templates[family] = model
		}
	}
	seen := make(map[string]struct{}, len(filtered))
	for _, model := range filtered {
		seen[normalizeAntigravityFetchedModelID(model.ID)] = struct{}{}
	}
	ids := make([]string, 0, len(hints.ModelIDs))
	for id := range hints.ModelIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, exists := seen[id]; exists {
			continue
		}
		family, effort := antigravityEffortVariant(id)
		if family == "" {
			continue
		}
		template := templates[family]
		if template == nil {
			// Tiered IDs select the same known family through another serving route.
			template = templates[strings.TrimSuffix(family, "-tiered")]
		}
		if template == nil || !slices.Contains(template.Thinking.Levels, effort) {
			continue
		}
		variant := cloneModelInfoForCatalogRoute(template)
		variant.ID, variant.Name = id, id
		name := strings.TrimSpace(template.DisplayName)
		if name == "" {
			name = family
		}
		for _, suffix := range []string{" (Low)", " (Medium)", " (High)", " (XHigh)"} {
			name = strings.TrimSuffix(name, suffix)
		}
		label := strings.ToUpper(effort[:1]) + effort[1:]
		if effort == "xhigh" {
			label = "XHigh"
		}
		variant.DisplayName = name + " (" + label + ")"
		variant.Description = variant.DisplayName
		thinking := *template.Thinking
		thinking.Levels = slices.Clone(template.Thinking.Levels)
		variant.Thinking = &thinking
		_, variant.SupportsWebSearch = hints.WebSearchModelIDs[id]
		filtered = append(filtered, &variant)
		seen[id] = struct{}{}
	}
	return filtered
}

func antigravityEffortVariant(id string) (family, effort string) {
	for _, level := range []string{"low", "medium", "high", "xhigh"} {
		if base, ok := strings.CutSuffix(id, "-"+level); ok && base != "" {
			return base, level
		}
	}
	return "", ""
}
