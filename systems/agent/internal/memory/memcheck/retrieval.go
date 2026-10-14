package memcheck

import (
	"context"
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/memory/claims"
)

// DefaultRetrievalTopK is the size of the search prefix a sampled claim must
// land in: "the top 3" of DESIGN.md §7.5.
const DefaultRetrievalTopK = 3

// DefaultRetrievalSample is how many claims the retrieval check examines when
// RetrievalOptions.Sample is unset. §7.5 says "N randomly sampled claims";
// the check samples deterministically instead (see CheckRetrieval) so repeated
// runs against the same root report the same sample, which is what makes a
// CI gate's misses comparable and hand-run failures narrowable.
const DefaultRetrievalSample = 20

// SemanticHit is one vector-store result the retrieval check judges. It
// carries only the fields the self-hit question needs, so callers can map
// whatever their store returns into it; the integration test adapts the embed
// package's results this way.
type SemanticHit struct {
	// ID is the point identity in the collection, for naming a miss.
	ID string
	// Score is the search score, if the store supplies one.
	Score float64
	// FilePath is the payload path of the chunk, as the scanner wrote it: for
	// the agent's memory source, the runtime path under the memory mount.
	FilePath string
	// Text is the chunk's stored text.
	Text string
}

// SemanticSearcher searches the semantic collection with raw query text and
// returns up to topK hits. The retrieval check holds this interface instead of
// any concrete client, so the check itself stays offline: the default test run
// uses fakes, and only the integration-tagged test constructs a searcher over
// a live Qdrant and embedder.
type SemanticSearcher interface {
	SearchSemantic(ctx context.Context, text string, topK int) ([]SemanticHit, error)
}

// SearchFunc adapts a function to SemanticSearcher, the way the integration
// test bridges the embed package's store and embedder into the check.
type SearchFunc func(ctx context.Context, text string, topK int) ([]SemanticHit, error)

// SearchSemantic calls f.
func (f SearchFunc) SearchSemantic(
	ctx context.Context,
	text string,
	topK int,
) ([]SemanticHit, error) {
	if f == nil {
		return nil, fmt.Errorf("memcheck: no semantic search was supplied")
	}
	return f(ctx, text, topK)
}

// RetrievalOptions tunes the retrieval sanity check.
type RetrievalOptions struct {
	// Sample caps how many claims are checked. When more claims exist than
	// Sample allows, the check examines an evenly strided, reproducible
	// subset across every file's claims in document order. Non-positive
	// values select DefaultRetrievalSample, and a Sample at or above the
	// claim count checks them all.
	Sample int
	// TopK is the size of the search prefix a self-hit must land in.
	// Non-positive values select DefaultRetrievalTopK.
	TopK int
}

// DefaultRetrievalOptions returns §7.5's shape: the top three hits must cover
// each of twenty sampled claims.
func DefaultRetrievalOptions() RetrievalOptions {
	return RetrievalOptions{Sample: DefaultRetrievalSample, TopK: DefaultRetrievalTopK}
}

// RetrievalMiss is one sampled claim the search did not surface in the top
// prefix, named with what the search returned instead so a failure can be
// investigated without rerunning.
type RetrievalMiss struct {
	// ClaimID, File and Text identify the sampled claim.
	ClaimID string
	File    string
	Text    string
	// Hits are the top results the search returned for the claim's own text.
	Hits []SemanticHit
}

// RetrievalReport is the corpus-level retrieval measurement of one root.
type RetrievalReport struct {
	// Root is the memory root the report was measured against.
	Root string
	// Sampled is how many claims were searched.
	Sampled int
	// TopK is the prefix size a self-hit had to land in.
	TopK int
	// Misses lists every sampled claim that failed, in sample order.
	Misses []RetrievalMiss
}

// OK reports whether every sampled claim came back within the top prefix.
func (r RetrievalReport) OK() bool {
	return len(r.Misses) == 0
}

// CheckRetrieval samples claims from a memory root's semantic files and
// searches the semantic collection with each claim's own text, requiring the
// claim itself to come back within the top TopK hits (DESIGN.md §7.5). It
// catches format regressions that break chunking: a claim whose text no
// longer reaches its own chunk cannot be retrieved by that text.
//
// A hit covers a claim when the hit's stored text contains the claim's ID and
// the hit's payload path is the claim's file. The scanner writes payload
// paths under the memory mount (cognition's runtime prefix), so the match
// also accepts the path as a suffix after a slash, covering both the runtime
// form and the bare relative form.
//
// Chunking keeps several claims in one section chunk, so the self-hit is the
// chunk the claim belongs to, not a point per claim.
//
// The check is read-only and needs no network itself: the caller supplies the
// SemanticSearcher. A root with no claims reports an empty sample rather than
// an error, since there is nothing to falsify yet; a nil searcher or a path
// that is not a memory root is an error worth naming.
func CheckRetrieval(
	ctx context.Context,
	root string,
	searcher SemanticSearcher,
	opts RetrievalOptions,
) (RetrievalReport, error) {
	if searcher == nil {
		return RetrievalReport{}, fmt.Errorf("memcheck: semantic searcher is required")
	}
	if opts.Sample <= 0 {
		opts.Sample = DefaultRetrievalSample
	}
	if opts.TopK <= 0 {
		opts.TopK = DefaultRetrievalTopK
	}
	files, err := semanticClaimFiles(root)
	if err != nil {
		return RetrievalReport{}, err
	}

	var all []retrievedClaim
	for _, file := range files {
		for _, claim := range file.claims {
			if strings.TrimSpace(claim.Text) == "" {
				continue
			}
			all = append(all, retrievedClaim{
				claim: claim,
				file:  file.relativePath,
			})
		}
	}
	sampled := sampleClaims(all, opts.Sample)
	report := RetrievalReport{
		Root:    root,
		Sampled: len(sampled),
		TopK:    opts.TopK,
	}
	for _, claim := range sampled {
		hits, err := searcher.SearchSemantic(ctx, claim.claim.Text, opts.TopK)
		if err != nil {
			return RetrievalReport{}, fmt.Errorf(
				"search semantic collection for claim %s: %w",
				claim.claim.ID,
				err,
			)
		}
		if hitsCoverClaim(hits, claim) {
			continue
		}
		report.Misses = append(report.Misses, RetrievalMiss{
			ClaimID: claim.claim.ID,
			File:    claim.file,
			Text:    claim.claim.Text,
			Hits:    hits,
		})
	}
	return report, nil
}

// retrievedClaim pairs a parsed claim with the file it was parsed from, the
// identity the self-hit match is judged against.
type retrievedClaim struct {
	claim claims.Claim
	file  string
}

// sampleClaims picks the claims to check: all of them unless more claims than
// the cap allows, in which case an evenly strided subset of exactly the cap's
// size, fixed by position so a rerun against the same content samples the
// same claims.
func sampleClaims(all []retrievedClaim, maxClaims int) []retrievedClaim {
	if maxClaims <= 0 || maxClaims >= len(all) {
		return all
	}
	step := len(all) / maxClaims
	sampled := make([]retrievedClaim, 0, maxClaims)
	for i := 0; i < maxClaims; i++ {
		sampled = append(sampled, all[i*step])
	}
	return sampled
}

// hitsCoverClaim reports whether any hit is the chunk the claim belongs to.
func hitsCoverClaim(hits []SemanticHit, claim retrievedClaim) bool {
	for _, hit := range hits {
		if hitCoversClaim(hit, claim) {
			return true
		}
	}
	return false
}

// hitCoversClaim matches one hit to one claim: the chunk's stored text must
// contain the claim ID, and the chunk's payload path must be the claim's file
// whether it was written with a runtime prefix or as the bare relative path.
func hitCoversClaim(hit SemanticHit, claim retrievedClaim) bool {
	if !strings.Contains(hit.Text, claim.claim.ID) {
		return false
	}
	if hit.FilePath == claim.file {
		return true
	}
	return strings.HasSuffix(hit.FilePath, "/"+claim.file)
}
