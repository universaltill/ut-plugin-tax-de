package auditkey

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func TestTSEResultKey_Deterministic(t *testing.T) {
	a := TSEResultKey("sale-0001")
	b := TSEResultKey("sale-0001")
	if a != b {
		t.Fatalf("same sale id produced different keys: %q vs %q", a, b)
	}
}

func TestTSEResultKey_Format(t *testing.T) {
	key := TSEResultKey("any-sale-id")
	const prefix = "tse_result:"
	if !strings.HasPrefix(key, prefix) {
		t.Fatalf("key %q missing prefix %q", key, prefix)
	}
	bucket, err := strconv.Atoi(strings.TrimPrefix(key, prefix))
	if err != nil {
		t.Fatalf("key %q has a non-numeric bucket suffix: %v", key, err)
	}
	if bucket < 0 || bucket >= RingSize {
		t.Fatalf("bucket %d out of bounded range [0, %d)", bucket, RingSize)
	}
}

// TestTSEResultKey_BoundedRegardlessOfVolume is the actual property
// ut-docs#1299 exists to guarantee: however many distinct sale ids are
// ever recorded, the set of *keys* produced can never exceed RingSize --
// this is what keeps plugin storage from ever again growing toward core's
// 1024-key-per-plugin StorageMaxKeys cap purely from sale volume.
func TestTSEResultKey_BoundedRegardlessOfVolume(t *testing.T) {
	seen := make(map[string]struct{})
	const sales = 50_000 // far more than any real till processes before decommission
	for i := 0; i < sales; i++ {
		seen[TSEResultKey(fmt.Sprintf("sale-%d", i))] = struct{}{}
	}
	if len(seen) > RingSize {
		t.Fatalf("got %d distinct keys across %d sale ids, want <= RingSize (%d)", len(seen), sales, RingSize)
	}
}

// TestTSEResultKey_Distributes guards against a degenerate hash that
// collapses everything into one or two buckets, which would defeat the
// point of a ring (every sale overwriting the same one or two keys
// instead of spreading recent results across the ring).
func TestTSEResultKey_Distributes(t *testing.T) {
	seen := make(map[string]struct{})
	const sales = 2000
	for i := 0; i < sales; i++ {
		seen[TSEResultKey(fmt.Sprintf("sale-%d", i))] = struct{}{}
	}
	// Not a precise bound -- just enough to catch "always bucket 0" class
	// bugs. With RingSize buckets and 2000 well-spread inputs we expect
	// the large majority of buckets to be hit at least once.
	const wantAtLeast = RingSize * 3 / 4
	if len(seen) < wantAtLeast {
		t.Fatalf("only %d distinct buckets hit across %d sale ids, want >= %d (poor distribution)", len(seen), sales, wantAtLeast)
	}
}

// TestTSEResultKey_DifferentIDsUsuallyDifferentBuckets pins two FIXED,
// known-non-colliding sale ids (confirmed against the real FNV-1a hash,
// not chosen blindly) into different buckets. Deliberately t.Fatalf, not
// t.Skip: since both inputs and the hash are fixed, this is deterministic
// -- not flaky -- so a regression that made TSEResultKey ignore its input
// entirely (returning a constant bucket for everything) must fail this
// test every time, not silently skip past it. (Caught in review: an
// earlier draft of this test used t.Skip on the "collision" branch, which
// can never fail and so proved nothing -- exactly the "test that cannot
// notice the code changing is not a test" trap this repo's own wasmrun
// suite doc comment warns about. TestTSEResultKey_Distributes already
// covers the general degenerate-hash case; this test adds a fast,
// specific, always-executed pin.)
func TestTSEResultKey_DifferentIDsUsuallyDifferentBuckets(t *testing.T) {
	a := TSEResultKey("sale-aaaa-1111")
	b := TSEResultKey("sale-bbbb-2222")
	if a == b {
		t.Fatalf("TSEResultKey(%q) == TSEResultKey(%q) == %q -- these two fixed ids are confirmed non-colliding under FNV-1a & 0xFF; a collision here means the function is ignoring its input", "sale-aaaa-1111", "sale-bbbb-2222", a)
	}
}
