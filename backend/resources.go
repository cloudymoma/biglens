package main

// Pure domain logic for the GCP Resources section: project validation,
// machine-type parsing, workload classification, asset summarization and
// insight rules. Everything here is unit-testable without GCP.

import "regexp"

// resourceProjectRe: plain GCP project ID (6-30 chars, lowercase letters,
// digits, hyphens; starts with a letter, doesn't end with a hyphen).
var resourceProjectRe = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

func validResourceProject(p string) bool {
	return resourceProjectRe.MatchString(p)
}
