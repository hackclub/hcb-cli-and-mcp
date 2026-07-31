package hcbapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCompactTransactionsFixture(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "transactions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var page Page
	if err := json.Unmarshal(fixture, &page); err != nil {
		t.Fatal(err)
	}

	compacted, err := CompactPage(&page)
	if err != nil {
		t.Fatal(err)
	}
	if compacted.TotalCount != page.TotalCount || compacted.HasMore != page.HasMore {
		t.Errorf("envelope fields not preserved")
	}

	var full, small []map[string]any
	if err := json.Unmarshal(page.Data, &full); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(compacted.Data, &small); err != nil {
		t.Fatal(err)
	}
	if len(small) != len(full) {
		t.Fatalf("compacted %d txns, want %d", len(small), len(full))
	}
	for i, c := range small {
		f := full[i]
		for _, k := range []string{"id", "date", "amount_cents", "memo", "code"} {
			if c[k] != f[k] {
				t.Errorf("txn %d: %s = %v, want %v", i, k, c[k], f[k])
			}
		}
		// nested type sub-objects must be summarized away
		for _, k := range txnTypeKeys {
			if _, ok := c[k]; ok {
				t.Errorf("txn %d: sub-object %s survived compaction", i, k)
			}
		}
	}
	if len(compacted.Data) >= len(page.Data) {
		t.Errorf("compacted data (%d bytes) not smaller than full (%d bytes)",
			len(compacted.Data), len(page.Data))
	}
}

func TestCompactTransactionShapes(t *testing.T) {
	in := []map[string]any{
		{
			"id": "txn_card", "date": "2026-07-01", "amount_cents": float64(-1000),
			"memo": "PIZZA", "code": "600",
			"pending": true, "declined": false, "missing_receipt": true,
			"tags": []any{map[string]any{"id": "tag_a", "label": "Food", "emoji": "🍕"}},
			"card_charge": map[string]any{
				"merchant": map[string]any{"name": "PIZZA LLC", "smart_name": "Pizza Place"},
				"card":     map[string]any{"user": map[string]any{"name": "Jane Hacker"}},
			},
		},
		{
			"id": "txn_ach", "date": "2026-07-02", "amount_cents": float64(-5000),
			"memo": "ACH", "code": "300",
			"ach_transfer": map[string]any{
				"recipient_name": "Vendor Inc",
				"sender":         map[string]any{"name": "Org Admin"},
			},
		},
		{
			"id": "txn_don", "date": "2026-07-03", "amount_cents": float64(2500),
			"memo": "Donation", "code": "200",
			"donation": map[string]any{"donor": map[string]any{"name": "Generous Person"}},
		},
		{
			"id": "txn_xfer", "date": "2026-07-04", "amount_cents": float64(-100),
			"memo": "Transfer", "code": "500",
			"transfer": map[string]any{
				"from": map[string]any{"name": "Org A"},
				"to":   map[string]any{"name": "Org B"},
			},
		},
		{
			// no sub-object at all — must not panic, no type key
			"id": "txn_bare", "date": "2026-07-05", "amount_cents": float64(1), "memo": "?", "code": "000",
		},
	}
	raw, _ := json.Marshal(in)
	out, err := CompactTransactions(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}

	want := []map[string]any{
		{"type": "card_charge", "counterparty": "Pizza Place", "by": "Jane Hacker"},
		{"type": "ach_transfer", "counterparty": "Vendor Inc", "by": "Org Admin"},
		{"type": "donation", "counterparty": "Generous Person"},
		{"type": "transfer", "counterparty": "Org A -> Org B"},
		{},
	}
	for i, w := range want {
		for k, v := range w {
			if got[i][k] != v {
				t.Errorf("txn %d: %s = %v, want %v", i, k, got[i][k], v)
			}
		}
	}

	// false booleans must be omitted, true ones kept
	if _, ok := got[0]["declined"]; ok {
		t.Error("declined=false should be omitted")
	}
	if got[0]["pending"] != true || got[0]["missing_receipt"] != true {
		t.Error("true booleans must be kept")
	}
	// tags become labels
	tags, _ := got[0]["tags"].([]any)
	if len(tags) != 1 || tags[0] != "Food" {
		t.Errorf("tags = %v, want [Food]", got[0]["tags"])
	}
	if _, ok := got[4]["type"]; ok {
		t.Error("bare txn should have no type")
	}
}
