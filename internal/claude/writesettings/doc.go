// Package writesettings is kiln's only writer of settings files:
// read-modify-write of <cwd>/.kiln/settings.local.json (permission rules)
// and ~/.kiln/settings.json (the /model default). kiln reads Claude Code's
// .claude settings but never writes them. Writes preserve every other key,
// dedup on add, create the .kiln directory (with a .gitignore of "*") if
// needed, and replace the file atomically with 2-space indentation and a
// trailing newline.
package writesettings
