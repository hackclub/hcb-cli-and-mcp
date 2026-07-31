package hcbapi

import (
	"encoding/json"
	"fmt"
)

// CompactTransactions rewrites a transactions array (the data field of a
// paged ledger response) into one small summary object per transaction:
// the scalars that matter for scanning (id, date, amount_cents, memo, code),
// tag labels, the transaction type, and a best-effort counterparty/actor
// pulled from the type-specific sub-object. False booleans and empty fields
// are omitted. Full detail for any single transaction is available from
// GetTransaction.
func CompactTransactions(data json.RawMessage) (json.RawMessage, error) {
	var txns []map[string]any
	if err := json.Unmarshal(data, &txns); err != nil {
		return nil, fmt.Errorf("compacting transactions: %w", err)
	}
	out := make([]map[string]any, 0, len(txns))
	for _, t := range txns {
		out = append(out, compactTransaction(t))
	}
	return json.Marshal(out)
}

// CompactPage returns a copy of p with its data array compacted.
func CompactPage(p *Page) (*Page, error) {
	data, err := CompactTransactions(p.Data)
	if err != nil {
		return nil, err
	}
	return &Page{TotalCount: p.TotalCount, HasMore: p.HasMore, Data: data}, nil
}

// txnTypeKeys are the type-specific sub-object keys the v4 API renders on a
// transaction, one per transaction at most (matching HCB's _transaction
// partial).
var txnTypeKeys = []string{
	"card_charge", "donation", "invoice", "check", "check_deposit",
	"ach_transfer", "wire_transfer", "wise_transfer", "transfer", "expense_payout",
}

func compactTransaction(t map[string]any) map[string]any {
	c := map[string]any{}
	for _, k := range []string{"id", "date", "amount_cents", "memo", "code"} {
		if v, ok := t[k]; ok {
			c[k] = v
		}
	}
	for _, k := range []string{"pending", "declined", "reversed", "missing_receipt", "lost_receipt"} {
		if v, _ := t[k].(bool); v {
			c[k] = true
		}
	}
	if tags, ok := t["tags"].([]any); ok && len(tags) > 0 {
		labels := make([]string, 0, len(tags))
		for _, tag := range tags {
			if m, ok := tag.(map[string]any); ok {
				if l, ok := m["label"].(string); ok && l != "" {
					labels = append(labels, l)
				}
			}
		}
		if len(labels) > 0 {
			c["tags"] = labels
		}
	}
	for _, k := range txnTypeKeys {
		sub, ok := t[k].(map[string]any)
		if !ok {
			continue
		}
		c["type"] = k
		if who := compactCounterparty(k, sub); who != "" {
			c["counterparty"] = who
		}
		if by := compactActor(k, sub); by != "" {
			c["by"] = by
		}
		break
	}
	return c
}

// nestedStr walks path through nested objects and returns the string at the
// end, or "" when any step is missing or the wrong type.
func nestedStr(m map[string]any, path ...string) string {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[p]
	}
	s, _ := cur.(string)
	return s
}

func compactCounterparty(kind string, sub map[string]any) string {
	switch kind {
	case "card_charge":
		if s := nestedStr(sub, "merchant", "smart_name"); s != "" {
			return s
		}
		return nestedStr(sub, "merchant", "name")
	case "donation":
		return nestedStr(sub, "donor", "name")
	case "invoice":
		return nestedStr(sub, "sponsor", "name")
	case "transfer":
		from, to := nestedStr(sub, "from", "name"), nestedStr(sub, "to", "name")
		if from != "" || to != "" {
			return from + " -> " + to
		}
		return ""
	default:
		return nestedStr(sub, "recipient_name")
	}
}

func compactActor(kind string, sub map[string]any) string {
	if kind == "card_charge" {
		return nestedStr(sub, "card", "user", "name")
	}
	return nestedStr(sub, "sender", "name")
}
