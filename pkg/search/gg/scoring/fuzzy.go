package scoring

import (
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode"
)

// MatchResult stores the score, matched byte positions, and target path.
type MatchResult struct {
	Path      string
	Score     int
	Positions []int
	Matched   bool
}

// FastSubsequenceCheck verifies if all runes of pattern exist in target in order (case-insensitive).
func FastSubsequenceCheck(pattern, target string) bool {
	if len(pattern) == 0 {
		return true
	}
	if len(pattern) > len(target) {
		return false
	}

	pLower := strings.ToLower(pattern)
	tLower := strings.ToLower(target)

	pIdx := 0
	pRunes := []rune(pLower)
	pLen := len(pRunes)

	for _, tr := range tLower {
		if tr == pRunes[pIdx] {
			pIdx++
			if pIdx == pLen {
				return true
			}
		}
	}
	return false
}

// isSeparator checks if character is a word or path delimiter.
func isSeparator(ch byte) bool {
	return ch == '/' || ch == '\\' || ch == '_' || ch == '-' || ch == '.' || ch == ' '
}

// bonusForIndex calculates boundary and contextual bonuses for index j in target.
func bonusForIndex(target string, j int, lastSlash int) int {
	if j == 0 {
		bonus := 25
		if lastSlash < 0 {
			bonus += 10
		}
		return bonus
	}

	bonus := 0
	prev := target[j-1]
	curr := target[j]

	if isSeparator(prev) {
		bonus += 25
	} else if unicode.IsLower(rune(prev)) && unicode.IsUpper(rune(curr)) {
		bonus += 20 // CamelCase
	}

	if j > lastSlash {
		bonus += 10 // In basename
	}

	return bonus
}

// FuzzyScore computes optimal subsequence alignment score and match positions using dynamic programming.
func FuzzyScore(pattern, target string) (score int, positions []int, matched bool) {
	if len(pattern) == 0 {
		return 0, nil, true
	}
	if !FastSubsequenceCheck(pattern, target) {
		return 0, nil, false
	}

	pLower := []rune(strings.ToLower(pattern))
	tLower := []rune(strings.ToLower(target))
	tRunes := []rune(target)

	M := len(pLower)
	N := len(tLower)

	if M == 0 {
		return 0, nil, true
	}
	if N == 0 {
		return 0, nil, false
	}

	lastSlash := strings.LastIndexAny(target, "/\\")

	const (
		gapStart = 5
		gapExt   = 1
		consec   = 15
	)

	// D[i][j]: best score aligning p[0..i] with t[0..j] where p[i] matches t[j]
	// S[i][j]: best score aligning p[0..i] with t[0..j] (prefix max)
	D := make([][]int, M)
	S := make([][]int, M)
	for i := range D {
		D[i] = make([]int, N)
		S[i] = make([]int, N)
		for j := range D[i] {
			D[i][j] = -1 << 20
			S[i][j] = -1 << 20
		}
	}

	for j := 0; j < N; j++ {
		if pLower[0] == tLower[j] {
			bonus := bonusForIndex(target, j, lastSlash)
			D[0][j] = bonus
		}
		if j == 0 {
			S[0][j] = D[0][j]
		} else {
			S[0][j] = S[0][j-1] - gapExt
			if D[0][j] > S[0][j] {
				S[0][j] = D[0][j]
			}
		}
	}

	for i := 1; i < M; i++ {
		for j := i; j < N; j++ {
			if pLower[i] == tLower[j] {
				prevD := D[i-1][j-1] + consec
				prevS := S[i-1][j-1] - gapStart
				bestPrev := prevD
				if prevS > bestPrev {
					bestPrev = prevS
				}
				bonus := bonusForIndex(target, j, lastSlash)
				D[i][j] = bestPrev + bonus
			}
			S[i][j] = S[i][j-1] - gapExt
			if D[i][j] > S[i][j] {
				S[i][j] = D[i][j]
			}
		}
	}

	// Find optimal match end position
	bestScore := -1 << 20
	bestEnd := -1
	for j := M - 1; j < N; j++ {
		if D[M-1][j] > bestScore {
			bestScore = D[M-1][j]
			bestEnd = j
		}
	}

	if bestScore <= (-1 << 19) {
		return 0, nil, false
	}

	// Backtrack to find matched indices
	positions = make([]int, M)
	currJ := bestEnd
	positions[M-1] = currJ

	for i := M - 1; i > 0; i-- {
		// Was it consecutive match (from D[i-1][currJ-1]) or gap (from S[i-1][k])?
		targetBonus := bonusForIndex(target, currJ, lastSlash)
		if currJ > 0 && D[i-1][currJ-1]+consec+targetBonus == D[i][currJ] {
			currJ--
		} else {
			// Find k < currJ where S[i-1][k] produced best
			found := false
			for k := currJ - 1; k >= i-1; k-- {
				if S[i-1][k]-gapStart+targetBonus == D[i][currJ] {
					// Found the best state in S[i-1]
					// Now find the j where D[i-1][j] equals S[i-1][k]
					for j := k; j >= i-1; j-- {
						if D[i-1][j]-(k-j)*gapExt == S[i-1][k] {
							currJ = j
							found = true
							break
						}
					}
					if found {
						break
					}
				}
			}
			if !found {
				currJ--
			}
		}
		positions[i-1] = currJ
	}

	// Additional bonuses
	baseName := filepath.Base(target)
	if strings.EqualFold(baseName, pattern) {
		bestScore += 100 // Exact filename match
	} else if strings.EqualFold(strings.TrimSuffix(baseName, filepath.Ext(baseName)), pattern) {
		bestScore += 80 // Exact filename without extension
	}

	// Length penalty (shorter paths preferred)
	bestScore -= len(tRunes) / 4

	return bestScore, positions, true
}

// ParallelMatchScores scores a slice of candidate paths across available CPU cores.
func ParallelMatchScores(pattern string, paths []string, concurrency int) []MatchResult {
	if concurrency <= 0 {
		concurrency = runtime.NumCPU()
	}

	n := len(paths)
	results := make([]MatchResult, n)
	if n == 0 {
		return results
	}

	var wg sync.WaitGroup
	chunkSize := (n + concurrency - 1) / concurrency

	for c := 0; c < concurrency; c++ {
		start := c * chunkSize
		end := start + chunkSize
		if start >= n {
			break
		}
		if end > n {
			end = n
		}

		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			for i := s; i < e; i++ {
				score, pos, matched := FuzzyScore(pattern, paths[i])
				results[i] = MatchResult{
					Path:      paths[i],
					Score:     score,
					Positions: pos,
					Matched:   matched,
				}
			}
		}(start, end)
	}

	wg.Wait()
	return results
}
