// Package localapp holds the assets embedded into the binary.
//
// All implementation lives under cmd/localapp and internal/. This package
// exists solely to go:embed files at the repository root (embed paths cannot
// reference anything above the package directory, so embedding
// `skills/localapp/SKILL.md` requires a package at the root).
package localapp

import _ "embed"

// SkillMD is the contents of SKILL.md for coding agents (distributed per
// DESIGN.md "Agent skill"). Following the single-binary
// principle, distributed files are embedded into the binary.
//
//go:embed skills/localapp/SKILL.md
var SkillMD string
