// Package auditkey holds the pure, host-independent half of this plugin's
// tse_result:* audit-record key derivation.
//
// It exists as its own package for the same reason src/fiscalsign and
// src/taxrate do: src/main.go is compiled only for GOOS=wasip1 (it
// declares //go:wasmimport host functions), so it cannot be unit-tested
// on the host at all. Everything in here is ordinary Go with no host
// dependency, so it runs under a normal `go test` (ut-docs#1299).
package auditkey

import (
	"fmt"
	"hash/fnv"
)

// RingSize bounds how many distinct tse_result:* keys this plugin will
// ever hold in plugin storage, regardless of how many sales the till
// processes over its lifetime.
//
// Before ut-docs#1299, recordResult wrote one NEW key per sale
// (tse_result:<sale_id>), forever -- a shop trading steadily exhausts
// core's 1024-key-per-plugin StorageMaxKeys cap (internal/data/
// plugin_repo.go) well within realistic operating timeframes (roughly a
// thousand sales, not a thousand days). Once hit, every subsequent
// storage_set for this key space fails, silently (see recordResult's own
// doc comment for why "silently" is the confirmed, not assumed, behavior
// today).
//
// tse_result:* has zero readers anywhere in this codebase as of this
// writing -- it exists for "a future report/reconciliation surface" that
// does not exist yet, NOT the system of record for signing results: core
// owns that (internal/data/fiscal_repo.go's fiscal_tse_signatures table,
// ADR-0044), independent of anything this plugin keeps in its own
// storage. So collapsing recent results into a small, fixed-size ring --
// rather than one key per sale forever -- costs the "future
// reconciliation surface" nothing it relies on today, and permanently
// removes the growth that would otherwise hit the cap: total usage from
// this source is capped at RingSize keys for the life of the till, no
// matter the sale volume.
//
// 256 is a power of two (cheap bitmask, no modulo bias) chosen to leave
// generous headroom under the 1024 cap for the plugin's other storage
// keys (the fiskaly auth-token cache, currently the only other key this
// plugin writes).
const RingSize = 256

// TSEResultKey deterministically maps a sale id onto one of RingSize
// plugin-storage keys. Two different sale ids CAN land on the same
// bucket (that's the point -- it bounds total storage), in which case
// the older record is simply overwritten by storage_set's own
// upsert semantics; nothing in this codebase reads by exact sale id
// today, so that's a safe trade, not a functional regression.
//
// "Ring" is loose terminology, worth being precise about: this retains
// whichever sale most recently hashed into each bucket, NOT the most
// recent RingSize sales overall -- a bucket's occupant only changes when
// a NEW sale happens to hash to that same bucket, which for RingSize=256
// and well-distributed sale ids means most buckets update roughly once
// per RingSize sales, not every sale. If a future reconciliation surface
// ever wants "the last N sales" specifically, this does not deliver
// that -- it would need a monotonic sequence key instead.
func TSEResultKey(saleID string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(saleID)) // hash.Hash.Write never returns an error
	bucket := h.Sum32() & (RingSize - 1)
	// Zero-padded to keep keys lexically sortable and fixed-width; not
	// load-bearing behavior, just readability for anyone inspecting
	// plugin_storage directly.
	return fmt.Sprintf("tse_result:%03d", bucket)
}
