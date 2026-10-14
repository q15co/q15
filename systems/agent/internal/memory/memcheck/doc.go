// Package memcheck measures a memory root against the claims DESIGN.md §7
// makes falsifiable: that the conversion pass produces deduplicated claim
// texts (§7.3), that those claims are retrievable by their own text (§7.5),
// and that every sized file stays under the budget or cap it is judged
// against.
//
// The measurements are read-only walks over a memory root's semantic files,
// and every threshold is a parameter, so the same helpers serve a fixture
// corpus in tests and a live root driven by hand. The package is a measurement
// suite, not a runtime component: nothing in the agent registers it or runs
// it, and its only import from the memory runtime is the claim-format package,
// so pointing a check at a root cannot move agent behavior.
//
// The retrieval check is the one measurement that needs services behind a
// root: it queries the vector store through a SemanticSearcher the caller
// supplies. This package never constructs a network client itself, so the
// default test run touches no network and no Qdrant; the live check is the
// integration-tagged test, following the repository's existing convention
// (an Integration-named test that skips until its environment variables are
// set, as provider/ollama's TestOllamaLocalIntegration does).
package memcheck
