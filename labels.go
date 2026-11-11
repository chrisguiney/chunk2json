package main

import (
	"fmt"
	"unicode/utf8"

	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
)

// Field names follow the Shelley..Conway CDDL (cardano-ledger
// eras/*/impl/cddl-files). Every labeller falls back to the generic value
// when the shape is not what it expects, so a malformed or unforeseen block
// still prints in full instead of losing data.

type fn func(any) any

type field struct {
	name string
	f    fn
}

func tidy(v any) any {
	switch x := v.(type) {
	case tagV:
		switch x.Num {
		case 258: // set
			return tidy(x.V)
		case 30: // unit interval / rational
			if a, ok := x.V.([]any); ok && len(a) == 2 {
				return fmt.Sprintf("%v/%v", a[0], a[1])
			}
		}
		return tagV{x.Num, tidy(x.V)}
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = tidy(e)
		}
		return out
	case cborMap:
		out := make(cborMap, len(x))
		for i, e := range x {
			out[i] = entry{tidy(e.K), tidy(e.V)}
		}
		return out
	}
	return v
}

func apply(f fn, v any) any {
	if f == nil {
		return tidy(v)
	}
	return f(v)
}

func asArr(v any) ([]any, bool) {
	if t, ok := v.(tagV); ok && t.Num == 258 {
		v = t.V
	}
	a, ok := v.([]any)
	return a, ok
}

func asMap(v any) (cborMap, bool) {
	if t, ok := v.(tagV); ok && t.Num == 258 {
		v = t.V
	}
	m, ok := v.(cborMap)
	return m, ok
}

func asU(v any) (uint64, bool) {
	u, ok := v.(uint64)
	return u, ok
}

// positional labels a fixed-layout array; surplus elements keep their index.
func positional(v any, fields ...field) any {
	a, ok := asArr(v)
	if !ok {
		return tidy(v)
	}
	o := obj{}
	for i, e := range a {
		if i < len(fields) {
			o = append(o, kv{fields[i].name, apply(fields[i].f, e)})
		} else {
			o = append(o, kv{fmt.Sprintf("field_%d", i), tidy(e)})
		}
	}
	return o
}

// keyed labels an integer-keyed map.
func keyed(v any, fields map[uint64]field) any {
	m, ok := asMap(v)
	if !ok {
		return tidy(v)
	}
	o := obj{}
	for _, e := range m {
		k, ok := asU(e.K)
		if f, known := fields[k]; ok && known {
			o = append(o, kv{f.name, apply(f.f, e.V)})
		} else {
			o = append(o, kv{fmt.Sprintf("key_%v", e.K), tidy(e.V)})
		}
	}
	return o
}

func listOf(f fn) fn {
	return func(v any) any {
		a, ok := asArr(v)
		if !ok {
			return tidy(v)
		}
		out := make([]any, len(a))
		for i, e := range a {
			out[i] = f(e)
		}
		return out
	}
}

// union labels a [type, fields...] tagged-union array.
func union(cases map[uint64]struct {
	name   string
	fields []field
}) fn {
	return func(v any) any {
		a, ok := asArr(v)
		if !ok || len(a) == 0 {
			return tidy(v)
		}
		t, ok := asU(a[0])
		c, known := cases[t]
		if !ok || !known {
			return tidy(v)
		}
		o := obj{{"type", c.name}}
		if inner, ok := positional(a[1:], c.fields...).(obj); ok {
			o = append(o, inner...)
		}
		return o
	}
}

type ucase = struct {
	name   string
	fields []field
}

func f(name string, fn fn) field { return field{name, fn} }

// ---- leaves ----

func address(v any) any {
	b, ok := v.(bytesV)
	if !ok {
		return tidy(v)
	}
	a, err := lcommon.NewAddressFromBytes(b)
	if err != nil {
		return obj{{"hex", b}, {"decode_error", err.Error()}}
	}
	return a.String()
}

func txIn(v any) any {
	a, ok := asArr(v)
	if !ok || len(a) != 2 {
		return tidy(v)
	}
	id, ok1 := a[0].(bytesV)
	idx, ok2 := asU(a[1])
	if !ok1 || !ok2 {
		return tidy(v)
	}
	return fmt.Sprintf("%x#%d", []byte(id), idx)
}

func rational(v any) any { return tidy(v) }

func coinMap(v any) any { // reward_account => coin
	m, ok := asMap(v)
	if !ok {
		return tidy(v)
	}
	out := []any{}
	for _, e := range m {
		out = append(out, obj{{"reward_account", address(e.K)}, {"amount", e.V}})
	}
	return out
}

func multiasset(v any) any {
	m, ok := asMap(v)
	if !ok {
		return tidy(v)
	}
	out := []any{}
	for _, p := range m {
		assets, ok := asMap(p.V)
		if !ok {
			out = append(out, obj{{"policy_id", p.K}, {"assets", tidy(p.V)}})
			continue
		}
		for _, a := range assets {
			o := obj{{"policy_id", p.K}, {"asset_name_hex", a.K}}
			if n, ok := a.K.(bytesV); ok && printable(n) {
				o = append(o, kv{"asset_name", string(n)})
			}
			o = append(o, kv{"quantity", a.V})
			out = append(out, o)
		}
	}
	return out
}

func printable(b []byte) bool {
	if len(b) == 0 || !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func value(v any) any {
	if u, ok := asU(v); ok {
		return obj{{"lovelace", u}}
	}
	return positional(v, f("lovelace", nil), f("assets", multiasset))
}

func embedded(v any) (any, bool) {
	t, ok := v.(tagV)
	if !ok || t.Num != 24 {
		return nil, false
	}
	b, ok := t.V.(bytesV)
	if !ok {
		return nil, false
	}
	inner, err := decodeCBOR(b)
	if err != nil {
		return nil, false
	}
	return inner, true
}

func wPlutusData(v any) any {
	switch x := v.(type) {
	case tagV:
		var c uint64
		switch {
		case x.Num >= 121 && x.Num <= 127:
			c = x.Num - 121
		case x.Num >= 1280 && x.Num <= 1400:
			c = x.Num - 1280 + 7
		case x.Num == 102:
			a, ok := x.V.([]any)
			if ok && len(a) == 2 {
				return obj{{"constructor", a[0]}, {"fields", wPlutusData(a[1])}}
			}
			return tidy(v)
		default:
			return tagV{x.Num, wPlutusData(x.V)}
		}
		return obj{{"constructor", c}, {"fields", wPlutusData(x.V)}}
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = wPlutusData(e)
		}
		return out
	case cborMap:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = obj{{"k", wPlutusData(e.K)}, {"v", wPlutusData(e.V)}}
		}
		return out
	}
	return v
}

func datumOption(v any) any {
	a, ok := asArr(v)
	if !ok || len(a) != 2 {
		return tidy(v)
	}
	switch t, _ := asU(a[0]); t {
	case 0:
		return obj{{"datum_hash", a[1]}}
	case 1:
		if inner, ok := embedded(a[1]); ok {
			return obj{{"inline_datum", wPlutusData(inner)}}
		}
	}
	return tidy(v)
}

var scriptLanguages = map[uint64]string{0: "native", 1: "plutus_v1", 2: "plutus_v2", 3: "plutus_v3"}

func script(v any) any {
	a, ok := asArr(v)
	if !ok || len(a) != 2 {
		return tidy(v)
	}
	t, _ := asU(a[0])
	lang, known := scriptLanguages[t]
	if !known {
		return tidy(v)
	}
	if t == 0 {
		return obj{{"language", lang}, {"script", wNativeScript(a[1])}}
	}
	return obj{{"language", lang}, {"script", a[1]}}
}

func scriptRef(v any) any {
	if inner, ok := embedded(v); ok {
		return script(inner)
	}
	return tidy(v)
}

func wNativeScript(v any) any {
	return union(map[uint64]ucase{
		0: {"signature", []field{f("key_hash", nil)}},
		1: {"all", []field{f("scripts", listOf(wNativeScript))}},
		2: {"any", []field{f("scripts", listOf(wNativeScript))}},
		3: {"at_least", []field{f("required", nil), f("scripts", listOf(wNativeScript))}},
		4: {"invalid_before", []field{f("slot", nil)}},
		5: {"invalid_hereafter", []field{f("slot", nil)}},
	})(v)
}

func txOut(v any) any {
	if _, ok := asMap(v); ok {
		return keyed(v, map[uint64]field{
			0: f("address", address),
			1: f("amount", value),
			2: f("datum", datumOption),
			3: f("script_ref", scriptRef),
		})
	}
	return positional(v, f("address", address), f("amount", value), f("datum_hash", nil))
}

// ---- certificates and governance ----

func wCredential(v any) any {
	a, ok := asArr(v)
	if !ok || len(a) != 2 {
		return tidy(v)
	}
	switch t, _ := asU(a[0]); t {
	case 0:
		return obj{{"key_hash", a[1]}}
	case 1:
		return obj{{"script_hash", a[1]}}
	}
	return tidy(v)
}

func drep(v any) any {
	a, ok := asArr(v)
	if !ok || len(a) == 0 {
		return tidy(v)
	}
	switch t, _ := asU(a[0]); t {
	case 0:
		if len(a) == 2 {
			return obj{{"key_hash", a[1]}}
		}
	case 1:
		if len(a) == 2 {
			return obj{{"script_hash", a[1]}}
		}
	case 2:
		return "always_abstain"
	case 3:
		return "always_no_confidence"
	}
	return tidy(v)
}

func wAnchor(v any) any {
	if v == nil {
		return nil
	}
	return positional(v, f("url", nil), f("data_hash", nil))
}

func wRelay(v any) any {
	return union(map[uint64]ucase{
		0: {"single_host_addr", []field{f("port", nil), f("ipv4", ipv4), f("ipv6", nil)}},
		1: {"single_host_name", []field{f("port", nil), f("dns_name", nil)}},
		2: {"multi_host_name", []field{f("dns_name", nil)}},
	})(v)
}

func ipv4(v any) any {
	if b, ok := v.(bytesV); ok && len(b) == 4 {
		return fmt.Sprintf("%d.%d.%d.%d", b[0], b[1], b[2], b[3])
	}
	return tidy(v)
}

func mir(v any) any {
	a, ok := asArr(v)
	if !ok || len(a) != 2 {
		return tidy(v)
	}
	pot := tidy(a[0])
	if u, ok := asU(a[0]); ok {
		pot = map[uint64]string{0: "reserves", 1: "treasury"}[u]
	}
	var target any
	if m, ok := asMap(a[1]); ok {
		rewards := []any{}
		for _, e := range m {
			rewards = append(rewards, obj{{"credential", wCredential(e.K)}, {"amount", e.V}})
		}
		target = obj{{"rewards", rewards}}
	} else {
		target = obj{{"transfer_to_other_pot", a[1]}}
	}
	return obj{{"pot", pot}, target.(obj)[0]}
}

func wCertificate(v any) any {
	cred := f("credential", wCredential)
	return union(map[uint64]ucase{
		0: {"stake_registration", []field{cred}},
		1: {"stake_deregistration", []field{cred}},
		2: {"stake_delegation", []field{cred, f("pool_key_hash", nil)}},
		3: {"pool_registration", []field{
			f("operator", nil), f("vrf_key_hash", nil), f("pledge", nil), f("cost", nil),
			f("margin", rational), f("reward_account", address), f("pool_owners", nil),
			f("relays", listOf(wRelay)), f("pool_metadata", wAnchor),
		}},
		4:  {"pool_retirement", []field{f("pool_key_hash", nil), f("epoch", nil)}},
		5:  {"genesis_key_delegation", []field{f("genesis_hash", nil), f("genesis_delegate_hash", nil), f("vrf_key_hash", nil)}},
		6:  {"move_instantaneous_rewards", []field{f("mir", mir)}},
		7:  {"reg_cert", []field{cred, f("deposit", nil)}},
		8:  {"unreg_cert", []field{cred, f("refund", nil)}},
		9:  {"vote_deleg_cert", []field{cred, f("drep", drep)}},
		10: {"stake_vote_deleg_cert", []field{cred, f("pool_key_hash", nil), f("drep", drep)}},
		11: {"stake_reg_deleg_cert", []field{cred, f("pool_key_hash", nil), f("deposit", nil)}},
		12: {"vote_reg_deleg_cert", []field{cred, f("drep", drep), f("deposit", nil)}},
		13: {"stake_vote_reg_deleg_cert", []field{cred, f("pool_key_hash", nil), f("drep", drep), f("deposit", nil)}},
		14: {"auth_committee_hot_cert", []field{f("committee_cold_credential", wCredential), f("committee_hot_credential", wCredential)}},
		15: {"resign_committee_cold_cert", []field{f("committee_cold_credential", wCredential), f("anchor", wAnchor)}},
		16: {"reg_drep_cert", []field{f("drep_credential", wCredential), f("deposit", nil), f("anchor", wAnchor)}},
		17: {"unreg_drep_cert", []field{f("drep_credential", wCredential), f("refund", nil)}},
		18: {"update_drep_cert", []field{f("drep_credential", wCredential), f("anchor", wAnchor)}},
	})(v)
}

func exUnits(v any) any { return positional(v, f("mem", nil), f("steps", nil)) }

func protocolVersion(v any) any { return positional(v, f("major", nil), f("minor", nil)) }

func paramUpdate(v any) any {
	return keyed(v, map[uint64]field{
		0: f("min_fee_a", nil), 1: f("min_fee_b", nil), 2: f("max_block_body_size", nil),
		3: f("max_tx_size", nil), 4: f("max_block_header_size", nil), 5: f("key_deposit", nil),
		6: f("pool_deposit", nil), 7: f("max_epoch", nil), 8: f("n_opt", nil),
		9: f("pool_pledge_influence", rational), 10: f("expansion_rate", rational),
		11: f("treasury_growth_rate", rational), 12: f("decentralization", rational),
		13: f("extra_entropy", nil), 14: f("protocol_version", protocolVersion),
		15: f("min_utxo_value", nil), 16: f("min_pool_cost", nil), 17: f("ada_per_utxo_byte", nil),
		18: f("cost_models", nil),
		19: f("execution_costs", func(v any) any { return positional(v, f("mem_price", rational), f("step_price", rational)) }),
		20: f("max_tx_ex_units", exUnits), 21: f("max_block_ex_units", exUnits),
		22: f("max_value_size", nil), 23: f("collateral_percentage", nil), 24: f("max_collateral_inputs", nil),
		25: f("pool_voting_thresholds", nil), 26: f("drep_voting_thresholds", nil),
		27: f("min_committee_size", nil), 28: f("committee_term_limit", nil),
		29: f("governance_action_validity_period", nil), 30: f("governance_action_deposit", nil),
		31: f("drep_deposit", nil), 32: f("drep_inactivity_period", nil),
		33: f("min_fee_ref_script_cost_per_byte", rational),
	})
}

func shelleyUpdate(v any) any {
	return positional(v,
		f("proposals", func(v any) any {
			m, ok := asMap(v)
			if !ok {
				return tidy(v)
			}
			out := []any{}
			for _, e := range m {
				out = append(out, obj{{"genesis_hash", e.K}, {"parameters", paramUpdate(e.V)}})
			}
			return out
		}),
		f("epoch", nil))
}

func wGovActionID(v any) any {
	if v == nil {
		return nil
	}
	return txIn(v)
}

func wGovAction(v any) any {
	prev := f("previous_action", wGovActionID)
	return union(map[uint64]ucase{
		0: {"parameter_change", []field{prev, f("parameters", paramUpdate), f("policy_hash", nil)}},
		1: {"hard_fork_initiation", []field{prev, f("protocol_version", protocolVersion)}},
		2: {"treasury_withdrawals", []field{f("withdrawals", coinMap), f("policy_hash", nil)}},
		3: {"no_confidence", []field{prev}},
		4: {"update_committee", []field{
			prev, f("remove", listOf(wCredential)),
			f("add", func(v any) any {
				m, ok := asMap(v)
				if !ok {
					return tidy(v)
				}
				out := []any{}
				for _, e := range m {
					out = append(out, obj{{"credential", wCredential(e.K)}, {"expires_epoch", e.V}})
				}
				return out
			}),
			f("threshold", rational),
		}},
		5: {"new_constitution", []field{prev, f("constitution", func(v any) any {
			return positional(v, f("anchor", wAnchor), f("script_hash", nil))
		})}},
		6: {"info_action", nil},
	})(v)
}

var voterRoles = map[uint64]string{
	0: "constitutional_committee_hot_key", 1: "constitutional_committee_hot_script",
	2: "drep_key", 3: "drep_script", 4: "stake_pool",
}

var wVotes = map[uint64]string{0: "no", 1: "yes", 2: "abstain"}

func votingProcedures(v any) any {
	m, ok := asMap(v)
	if !ok {
		return tidy(v)
	}
	out := []any{}
	for _, e := range m {
		voter := tidy(e.K)
		if a, ok := asArr(e.K); ok && len(a) == 2 {
			if r, ok := asU(a[0]); ok {
				voter = obj{{"role", voterRoles[r]}, {"hash", a[1]}}
			}
		}
		ballots := []any{}
		if inner, ok := asMap(e.V); ok {
			for _, b := range inner {
				o := obj{{"action", wGovActionID(b.K)}}
				if p, ok := asArr(b.V); ok && len(p) == 2 {
					vote := tidy(p[0])
					if u, ok := asU(p[0]); ok {
						vote = wVotes[u]
					}
					o = append(o, kv{"vote", vote}, kv{"anchor", wAnchor(p[1])})
				} else {
					o = append(o, kv{"procedure", tidy(b.V)})
				}
				ballots = append(ballots, o)
			}
		}
		out = append(out, obj{{"voter", voter}, {"votes", ballots}})
	}
	return out
}

func proposal(v any) any {
	return positional(v, f("deposit", nil), f("return_address", address), f("action", wGovAction), f("anchor", wAnchor))
}

// ---- transactions ----

func txBody(v any) any {
	ins := listOf(txIn)
	return keyed(v, map[uint64]field{
		0: f("inputs", ins), 1: f("outputs", listOf(txOut)), 2: f("fee", nil), 3: f("ttl", nil),
		4: f("certificates", listOf(wCertificate)), 5: f("withdrawals", coinMap),
		6: f("update", shelleyUpdate), 7: f("auxiliary_data_hash", nil),
		8: f("validity_interval_start", nil), 9: f("mint", multiasset),
		11: f("script_data_hash", nil), 13: f("collateral_inputs", ins),
		14: f("required_signers", nil), 15: f("network_id", nil),
		16: f("collateral_return", txOut), 17: f("total_collateral", nil),
		18: f("reference_inputs", ins), 19: f("voting_procedures", votingProcedures),
		20: f("proposal_procedures", listOf(proposal)),
		21: f("current_treasury_value", nil), 22: f("donation", nil),
	})
}

var redeemerTags = map[uint64]string{0: "spend", 1: "mint", 2: "certify", 3: "reward", 4: "vote", 5: "propose"}

func redeemerPurpose(v any) any {
	if u, ok := asU(v); ok {
		if s, ok := redeemerTags[u]; ok {
			return s
		}
	}
	return tidy(v)
}

func redeemers(v any) any {
	if m, ok := asMap(v); ok { // Conway map form: [tag, index] => [data, ex_units]
		out := []any{}
		for _, e := range m {
			k, ok1 := asArr(e.K)
			val, ok2 := asArr(e.V)
			if !ok1 || !ok2 || len(k) != 2 || len(val) != 2 {
				out = append(out, obj{{"k", tidy(e.K)}, {"v", tidy(e.V)}})
				continue
			}
			out = append(out, obj{
				{"purpose", redeemerPurpose(k[0])}, {"index", k[1]},
				{"data", wPlutusData(val[0])}, {"ex_units", exUnits(val[1])},
			})
		}
		return out
	}
	return listOf(func(v any) any {
		return positional(v, f("purpose", redeemerPurpose), f("index", nil), f("data", wPlutusData), f("ex_units", exUnits))
	})(v)
}

func witnessSet(v any) any {
	return keyed(v, map[uint64]field{
		0: f("vkey_witnesses", listOf(func(v any) any { return positional(v, f("vkey", nil), f("signature", nil)) })),
		1: f("native_scripts", listOf(wNativeScript)),
		2: f("bootstrap_witnesses", listOf(func(v any) any {
			return positional(v, f("public_key", nil), f("signature", nil), f("chain_code", nil), f("attributes", nil))
		})),
		3: f("plutus_v1_scripts", nil),
		4: f("plutus_data", listOf(wPlutusData)),
		5: f("redeemers", redeemers),
		6: f("plutus_v2_scripts", nil),
		7: f("plutus_v3_scripts", nil),
	})
}

func wAuxData(v any) any {
	if t, ok := v.(tagV); ok && t.Num == 259 { // Alonzo+
		return keyed(t.V, map[uint64]field{
			0: f("metadata", nil), 1: f("native_scripts", listOf(wNativeScript)),
			2: f("plutus_v1_scripts", nil), 3: f("plutus_v2_scripts", nil), 4: f("plutus_v3_scripts", nil),
		})
	}
	if _, ok := asArr(v); ok { // Allegra/Mary
		return positional(v, f("metadata", nil), f("native_scripts", listOf(wNativeScript)))
	}
	return obj{{"metadata", tidy(v)}}
}

// ---- header and block ----

func vrfCert(v any) any { return positional(v, f("output", nil), f("proof", nil)) }

func headerBody(v any) any {
	a, ok := asArr(v)
	if !ok {
		return tidy(v)
	}
	if len(a) == 15 { // Shelley..Alonzo: two VRF certs, flattened opcert and version
		return positional(v,
			f("block_number", nil), f("slot", nil), f("prev_hash", nil), f("issuer_vkey", nil),
			f("vrf_vkey", nil), f("nonce_vrf", vrfCert), f("leader_vrf", vrfCert),
			f("block_body_size", nil), f("block_body_hash", nil),
			f("op_cert_hot_vkey", nil), f("op_cert_sequence_number", nil),
			f("op_cert_kes_period", nil), f("op_cert_sigma", nil),
			f("protocol_major", nil), f("protocol_minor", nil))
	}
	return positional(v,
		f("block_number", nil), f("slot", nil), f("prev_hash", nil), f("issuer_vkey", nil),
		f("vrf_vkey", nil), f("vrf_result", vrfCert),
		f("block_body_size", nil), f("block_body_hash", nil),
		f("operational_cert", func(v any) any {
			return positional(v, f("hot_vkey", nil), f("sequence_number", nil), f("kes_period", nil), f("sigma", nil))
		}),
		f("protocol_version", protocolVersion))
}

// labelBlock turns a decoded Shelley..Conway block into named fields and
// zips its parallel arrays (bodies, witness sets, auxiliary data, invalid
// indices) into one record per transaction. txHashes, when it has one entry
// per body, supplies each transaction's id.
func labelBlock(eraTag uint, v any, txHashes []string) any {
	if eraTag < 2 {
		return obj{{"note", "Byron blocks are not labelled; generic CBOR follows"}, {"raw", tidy(v)}}
	}
	a, ok := asArr(v)
	if !ok || len(a) < 4 {
		return tidy(v)
	}
	header := positional(a[0], f("body", headerBody), f("body_signature", nil))
	bodies, ok1 := asArr(a[1])
	wits, ok2 := asArr(a[2])
	aux, _ := asMap(a[3])
	if !ok1 || !ok2 {
		return obj{{"header", header}, {"body", tidy(a[1:])}}
	}
	invalid := map[uint64]bool{}
	if len(a) > 4 {
		if ia, ok := asArr(a[4]); ok {
			for _, i := range ia {
				if u, ok := asU(i); ok {
					invalid[u] = true
				}
			}
		}
	}
	auxByIdx := map[uint64]any{}
	for _, e := range aux {
		if u, ok := asU(e.K); ok {
			auxByIdx[u] = e.V
		}
	}
	txs := make([]any, len(bodies))
	for i, b := range bodies {
		o := obj{{"index", i}}
		if len(txHashes) == len(bodies) {
			o = append(o, kv{"id", txHashes[i]})
		}
		o = append(o, kv{"valid", !invalid[uint64(i)]}, kv{"body", txBody(b)})
		if i < len(wits) {
			o = append(o, kv{"witnesses", witnessSet(wits[i])})
		}
		if ad, ok := auxByIdx[uint64(i)]; ok {
			o = append(o, kv{"auxiliary_data", wAuxData(ad)})
		}
		txs[i] = o
	}
	out := obj{{"header", header}, {"transactions", txs}}
	if len(wits) != len(bodies) {
		out = append(out, kv{"witness_sets_raw", tidy(a[2])})
	}
	return out
}
