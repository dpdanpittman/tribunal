package chain

import (
	"testing"

	"pgregory.net/rapid"
)

// TestPBT_ChunkFindingCommits_Invariants — properties of the F-OPUS-003
// chunking helper that v0.3.5 added. The chunking has to satisfy three
// invariants regardless of input size; rapid blasts random sizes
// (including the boundary cases the table-driven test hits, plus much
// larger sizes the table didn't) and shrinks failures.
//
// Properties under test (all must hold for any input commits slice):
//
//  1. Concatenating the chunks back together yields the original slice
//     in original order. No commit is lost or duplicated.
//  2. Every chunk's length is ≤ maxBatchChunkSize.
//  3. Every chunk except possibly the last is exactly maxBatchChunkSize.
//     (The last chunk may be partial.)
//  4. The number of chunks is ceil(N / maxBatchChunkSize), or exactly 1
//     when N ≤ maxBatchChunkSize.
func TestPBT_ChunkFindingCommits_Invariants(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		size := rapid.IntRange(0, 1000).Draw(t, "size")
		commits := make([]FindingCommit, size)
		for i := range commits {
			// Give each a unique FindingID so we can verify ordering
			// invariant via id comparison.
			commits[i] = FindingCommit{
				FindingID: testIDForIndex(i),
				PlanID:    "P-PBT",
			}
		}

		chunks := chunkFindingCommits(commits)

		// Property 1: concat back to original.
		flat := make([]FindingCommit, 0, size)
		for _, c := range chunks {
			flat = append(flat, c...)
		}
		if len(flat) != size {
			t.Fatalf("flatten len=%d want %d", len(flat), size)
		}
		for i := range flat {
			if flat[i].FindingID != testIDForIndex(i) {
				t.Fatalf("order violated at i=%d: got %q want %q", i, flat[i].FindingID, testIDForIndex(i))
			}
		}

		// Property 2: every chunk ≤ max.
		for i, c := range chunks {
			if len(c) > maxBatchChunkSize {
				t.Fatalf("chunk[%d] len=%d exceeds max %d", i, len(c), maxBatchChunkSize)
			}
		}

		// Property 3: every chunk except possibly last is exactly max.
		// Edge case: size=0 returns one empty chunk; allow that.
		if size > maxBatchChunkSize {
			for i := 0; i < len(chunks)-1; i++ {
				if len(chunks[i]) != maxBatchChunkSize {
					t.Fatalf("non-terminal chunk[%d] len=%d want %d", i, len(chunks[i]), maxBatchChunkSize)
				}
			}
		}

		// Property 4: chunk count.
		wantChunks := 1
		if size > maxBatchChunkSize {
			wantChunks = (size + maxBatchChunkSize - 1) / maxBatchChunkSize
		}
		if len(chunks) != wantChunks {
			t.Fatalf("chunk count=%d want %d (size=%d)", len(chunks), wantChunks, size)
		}
	})
}

// TestPBT_ChunkResolutionCommits_Invariants — same properties on the
// resolution side. Separate test (and separate generator data) so a
// regression in one slice type doesn't mask the other.
func TestPBT_ChunkResolutionCommits_Invariants(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		size := rapid.IntRange(0, 1000).Draw(t, "size")
		resCommits := make([]ResolutionCommit, size)
		for i := range resCommits {
			resCommits[i] = ResolutionCommit{
				FindingID: testIDForIndex(i),
				PlanID:    "P-PBT",
			}
		}

		chunks := chunkResolutionCommits(resCommits)

		flat := make([]ResolutionCommit, 0, size)
		for _, c := range chunks {
			flat = append(flat, c...)
		}
		if len(flat) != size {
			t.Fatalf("flatten len=%d want %d", len(flat), size)
		}
		for i := range flat {
			if flat[i].FindingID != testIDForIndex(i) {
				t.Fatalf("order violated at i=%d", i)
			}
		}
		for i, c := range chunks {
			if len(c) > maxBatchChunkSize {
				t.Fatalf("chunk[%d] len=%d exceeds max", i, len(c))
			}
		}
		if size > maxBatchChunkSize {
			for i := 0; i < len(chunks)-1; i++ {
				if len(chunks[i]) != maxBatchChunkSize {
					t.Fatalf("non-terminal chunk[%d] len=%d want %d", i, len(chunks[i]), maxBatchChunkSize)
				}
			}
		}
		wantChunks := 1
		if size > maxBatchChunkSize {
			wantChunks = (size + maxBatchChunkSize - 1) / maxBatchChunkSize
		}
		if len(chunks) != wantChunks {
			t.Fatalf("chunk count=%d want %d", len(chunks), wantChunks)
		}
	})
}

func testIDForIndex(i int) string {
	// Stable per-index id so ordering checks have a witness.
	const hex = "0123456789abcdef"
	out := make([]byte, 0, 16)
	out = append(out, "F-"...)
	// Encode i as hex into the suffix; fixed-width so lexical and
	// numerical order match for i in [0, 65535].
	for j := 12; j >= 0; j -= 4 {
		out = append(out, hex[(i>>uint(j))&0xF])
	}
	return string(out)
}
