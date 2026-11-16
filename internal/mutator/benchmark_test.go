package mutator_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/noissefnoc/bashtion/internal/mutator"
)

func generateSyntheticBashScript(numFuncs int) []byte {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	for i := 0; i < numFuncs; i++ {
		fmt.Fprintf(&b, `
worker_func_%d() {
    local input="${1:-default_%d}"
    local retries="$2"
    if [[ -z "$input" ]] || [[ -f "/tmp/lock_%d" ]]; then
        return 1
    fi
    if [[ "$retries" -ge 3 ]] && (( retries + 1 < 10 )); then
        return 0
    fi
    return 1
}
`, i, i, i)
	}
	return []byte(b.String())
}

func BenchmarkGenerate_Small_10Funcs(b *testing.B) {
	src := generateSyntheticBashScript(10) // ~100 mutants
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mutants, err := mutator.Generate("small.sh", src)
		if err != nil || len(mutants) == 0 {
			b.Fatalf("err=%v len=%d", err, len(mutants))
		}
	}
}

func BenchmarkGenerate_Large_50Funcs(b *testing.B) {
	src := generateSyntheticBashScript(50) // ~500 mutants
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mutants, err := mutator.Generate("large.sh", src)
		if err != nil || len(mutants) == 0 {
			b.Fatalf("err=%v len=%d", err, len(mutants))
		}
	}
}

func BenchmarkUnifiedDiff_Large(b *testing.B) {
	src := generateSyntheticBashScript(50)
	mutants, err := mutator.Generate("large.sh", src)
	if err != nil {
		b.Fatal(err)
	}
	targetMutant := mutants[len(mutants)/2]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = targetMutant.UnifiedDiff(src)
	}
}
