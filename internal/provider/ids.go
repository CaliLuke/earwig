package provider

import "regexp"

var claudeID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var codexID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

func ValidClaudeID(v string) bool { return claudeID.MatchString(v) }
func ValidCodexID(v string) bool  { return codexID.MatchString(v) }
