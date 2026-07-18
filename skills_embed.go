// Package pairmux (the module root) exists solely to embed the canonical
// pairmux agent skill: go:embed paths cannot traverse "..", so the embed must
// live beside the skills/ directory it captures. Nothing else lives here.
package pairmux

import "embed"

// SkillFS holds the canonical agent skill tree (skills/pairmux: SKILL.md plus
// references/), synced from the pairmux-skills repo at release time. `pairmux
// skill install` copies it into each agent's skills directory.
//
//go:embed skills/pairmux
var SkillFS embed.FS
