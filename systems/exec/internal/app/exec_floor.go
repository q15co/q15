package app

import _ "embed"

// execFloorDefinition is generated from the repository's single floor list.
//
//go:generate cp ../../../../docker/exec-floor.txt exec-floor.txt
//go:embed exec-floor.txt
var execFloorDefinition string
