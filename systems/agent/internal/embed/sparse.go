package embed

import (
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"unicode"
)

var sparseStopWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "but": {},
	"by": {}, "for": {}, "from": {}, "has": {}, "have": {}, "in": {}, "into": {},
	"is": {}, "it": {}, "its": {}, "of": {}, "on": {}, "or": {}, "that": {},
	"the": {}, "their": {}, "then": {}, "there": {}, "this": {}, "to": {},
	"was": {}, "were": {}, "with": {},
}

func encodeSparseText(text string) SparseVector {
	counts := make(map[uint32]int)
	for _, token := range sparseTokens(text) {
		if _, stop := sparseStopWords[token]; stop {
			continue
		}
		counts[sparseTokenIndex(token)]++
	}
	if len(counts) == 0 {
		return SparseVector{}
	}

	indices := make([]uint32, 0, len(counts))
	for index := range counts {
		indices = append(indices, index)
	}
	sort.Slice(indices, func(i, j int) bool {
		return indices[i] < indices[j]
	})

	values := make([]float32, 0, len(indices))
	var norm float64
	for _, index := range indices {
		value := 1 + math.Log(float64(counts[index]))
		norm += value * value
		values = append(values, float32(value))
	}
	if norm > 0 {
		scale := float32(math.Sqrt(norm))
		for i := range values {
			values[i] /= scale
		}
	}
	return SparseVector{Indices: indices, Values: values}
}

func sparseTokens(text string) []string {
	var tokens []string
	var token strings.Builder
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			token.WriteRune(r)
			continue
		}
		tokens = appendSparseToken(tokens, token.String())
		token.Reset()
	}
	return appendSparseToken(tokens, token.String())
}

func appendSparseToken(tokens []string, token string) []string {
	token = strings.TrimSpace(token)
	if token == "" {
		return tokens
	}
	return append(tokens, token)
}

func sparseTokenIndex(token string) uint32 {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(token))
	index := hash.Sum32()
	if index == 0 {
		return 1
	}
	return index
}
