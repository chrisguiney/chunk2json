package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"strings"
	"unicode"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/plutigo/data"
)

// GovActionView carries every governance action kind; Type says which.
type GovActionView struct {
	Type            string           `json:"type"`
	PreviousAction  string           `json:"previous_action,omitempty"`
	Parameters      any              `json:"parameters,omitempty"`
	PolicyHash      string           `json:"policy_hash,omitempty"`
	ProtocolVersion *VersionView     `json:"protocol_version,omitempty"`
	Withdrawals     []WithdrawalView `json:"withdrawals,omitempty"`
	Remove          []CredentialView `json:"remove,omitempty"`
	Add             []CommitteeAdd   `json:"add,omitempty"`
	Quorum          string           `json:"quorum,omitempty"`
	Constitution    *AnchorView      `json:"constitution,omitempty"`
	Unrecognised    any              `json:"unrecognised,omitempty"`
}

type CommitteeAdd struct {
	Credential   CredentialView `json:"credential"`
	ExpiresEpoch uint64         `json:"expires_epoch"`
}

// ---- small helpers ----

// deref returns the value a pointer points at, so type switches need only
// list value types; gouroboros returns some of these by pointer, some not.
func deref(v any) any {
	rv := reflect.ValueOf(v)
	for rv.IsValid() && rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return nil
	}
	return rv.Interface()
}

func hx(b []byte) string { return hex.EncodeToString(b) }

func num(b *big.Int) json.Number {
	if b == nil {
		return ""
	}
	return json.Number(b.String())
}

func numU(u uint64) json.Number { return json.Number(fmt.Sprint(u)) }

func rat(r *big.Rat) string {
	if r == nil {
		return ""
	}
	return r.Num().String() + "/" + r.Denom().String()
}

func u64(u uint64) *uint64 { return &u }

func printableName(b []byte) string {
	if printable(b) {
		return string(b)
	}
	return ""
}

func input(in lcommon.TransactionInput) string {
	id := in.Id()
	return fmt.Sprintf("%x#%d", id[:], in.Index())
}

func inputs(ins []lcommon.TransactionInput) []string {
	if len(ins) == 0 {
		return nil
	}
	out := make([]string, len(ins))
	for i, in := range ins {
		out[i] = input(in)
	}
	return out
}

func credential(c lcommon.Credential) CredentialView {
	h := c.Credential.String()
	if c.CredType == 1 {
		return CredentialView{ScriptHash: h}
	}
	return CredentialView{KeyHash: h}
}

func credPtr(c lcommon.Credential) *CredentialView { v := credential(c); return &v }

func anchor(a *lcommon.GovAnchor) *AnchorView {
	if a == nil {
		return nil
	}
	return &AnchorView{URL: a.Url, DataHash: hx(a.DataHash[:])}
}

func drepView(d lcommon.Drep) *DrepView {
	switch d.Type {
	case lcommon.DrepTypeAddrKeyHash:
		return &DrepView{Type: "key_hash", KeyHash: hx(d.Credential)}
	case lcommon.DrepTypeScriptHash:
		return &DrepView{Type: "script_hash", ScriptHash: hx(d.Credential)}
	case lcommon.DrepTypeAbstain:
		return &DrepView{Type: "always_abstain"}
	case lcommon.DrepTypeNoConfidence:
		return &DrepView{Type: "always_no_confidence"}
	}
	return &DrepView{Type: fmt.Sprintf("unknown_%d", d.Type), KeyHash: hx(d.Credential)}
}

func govActionID(id *lcommon.GovActionId) string {
	if id == nil {
		return ""
	}
	return fmt.Sprintf("%x#%d", id.TransactionId[:], id.GovActionIdx)
}

// ---- assets ----

type assetSource interface {
	Policies() []lcommon.Blake2b224
	Assets(lcommon.Blake2b224) [][]byte
}

func assetList[T any](m assetSource, qty func(lcommon.Blake2b224, []byte) T) []AssetView {
	if m == nil || reflect.ValueOf(m).IsNil() {
		return nil
	}
	var out []AssetView
	for _, p := range m.Policies() {
		for _, name := range m.Assets(p) {
			out = append(out, AssetView{
				PolicyID:     p.String(),
				AssetName:    printableName(name),
				AssetNameHex: hx(name),
				Quantity:     json.Number(fmt.Sprint(qty(p, name))),
			})
		}
	}
	// Policies and Assets come from Go maps, so their order changes run to
	// run; sort to keep the output stable.
	sort.Slice(out, func(i, j int) bool {
		if out[i].PolicyID != out[j].PolicyID {
			return out[i].PolicyID < out[j].PolicyID
		}
		return out[i].AssetNameHex < out[j].AssetNameHex
	})
	return out
}

// ---- outputs, datums, scripts ----

func output(o lcommon.TransactionOutput) OutputView {
	ov := OutputView{Address: o.Address().String(), Lovelace: num(o.Amount())}
	if a := o.Assets(); a != nil {
		ov.Assets = assetList(a, a.Asset)
	}
	// DatumHash also returns the hash of an inline datum, so it is shown
	// only when the output carries a hash and no datum.
	if d := o.Datum(); d != nil && d.Data != nil {
		ov.InlineDatum = plutusData(d.Data)
	} else if h := o.DatumHash(); h != nil && *h != (lcommon.Blake2b256{}) {
		ov.DatumHash = h.String()
	}
	if s := o.ScriptRef(); s != nil {
		ov.ScriptRef = scriptView(s)
	}
	return ov
}

func scriptView(s lcommon.Script) *ScriptView {
	sv := &ScriptView{Hash: s.Hash().String()}
	switch x := deref(s).(type) {
	case lcommon.NativeScript:
		sv.Language = "native"
		n := nativeScript(x)
		sv.Native = &n
	case lcommon.PlutusV1Script:
		sv.Language, sv.Bytes = "plutus_v1", hx(x)
	case lcommon.PlutusV2Script:
		sv.Language, sv.Bytes = "plutus_v2", hx(x)
	case lcommon.PlutusV3Script:
		sv.Language, sv.Bytes = "plutus_v3", hx(x)
	default:
		sv.Language, sv.Bytes = fmt.Sprintf("%T", x), hx(s.RawScriptBytes())
	}
	return sv
}

func nativeScript(n lcommon.NativeScript) NativeScriptView {
	list := func(ss []lcommon.NativeScript) []NativeScriptView {
		out := make([]NativeScriptView, len(ss))
		for i, s := range ss {
			out[i] = nativeScript(s)
		}
		return out
	}
	switch x := deref(n.Item()).(type) {
	case lcommon.NativeScriptPubkey:
		return NativeScriptView{Type: "signature", KeyHash: hx(x.Hash)}
	case lcommon.NativeScriptAll:
		return NativeScriptView{Type: "all", Scripts: list(x.Scripts)}
	case lcommon.NativeScriptAny:
		return NativeScriptView{Type: "any", Scripts: list(x.Scripts)}
	case lcommon.NativeScriptNofK:
		r := x.N
		return NativeScriptView{Type: "at_least", Required: &r, Scripts: list(x.Scripts)}
	case lcommon.NativeScriptInvalidBefore:
		return NativeScriptView{Type: "invalid_before", Slot: u64(x.Slot)}
	case lcommon.NativeScriptInvalidHereafter:
		return NativeScriptView{Type: "invalid_hereafter", Slot: u64(x.Slot)}
	case lcommon.NativeScriptRequireGuard:
		return NativeScriptView{Type: "require_guard", Credential: credPtr(x.Credential)}
	default:
		return NativeScriptView{Type: fmt.Sprintf("unknown %T", x)}
	}
}

func nativeScripts(ss []lcommon.NativeScript) []NativeScriptView {
	if len(ss) == 0 {
		return nil
	}
	out := make([]NativeScriptView, len(ss))
	for i, s := range ss {
		out[i] = nativeScript(s)
	}
	return out
}

func scriptBytes[T ~[]byte](ss []T) []string {
	if len(ss) == 0 {
		return nil
	}
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = hx(s)
	}
	return out
}

// plutusData shows constructors as {"constructor","fields"} and maps as
// [{"k","v"}], since Plutus map keys can be any datum.
func plutusData(d data.PlutusData) any {
	switch x := deref(d).(type) {
	case data.Constr:
		fields := make([]any, len(x.Fields))
		for i, f := range x.Fields {
			fields[i] = plutusData(f)
		}
		return obj{{"constructor", num(x.Tag)}, {"fields", fields}}
	case data.Map:
		out := make([]any, len(x.Pairs))
		for i, p := range x.Pairs {
			out[i] = obj{{"k", plutusData(p[0])}, {"v", plutusData(p[1])}}
		}
		return out
	case data.List:
		out := make([]any, len(x.Items))
		for i, e := range x.Items {
			out[i] = plutusData(e)
		}
		return out
	case data.Integer:
		return num(x.Inner)
	case data.ByteString:
		return hx(x.Inner)
	case nil:
		return nil
	default:
		return fmt.Sprintf("unhandled plutus data %T", x)
	}
}

// metadatum follows cardano-cli's no-schema JSON: byte strings are "0x…",
// maps with only text/int keys are objects, other maps are [{"k","v"}].
func metadatum(m lcommon.TransactionMetadatum) any {
	switch x := deref(m).(type) {
	case lcommon.MetaInt:
		return num(x.Value)
	case lcommon.MetaBytes:
		return "0x" + hx(x.Value)
	case lcommon.MetaText:
		return x.Value
	case lcommon.MetaList:
		out := make([]any, len(x.Items))
		for i, e := range x.Items {
			out[i] = metadatum(e)
		}
		return out
	case lcommon.MetaMap:
		simple := true
		for _, p := range x.Pairs {
			switch deref(p.Key).(type) {
			case lcommon.MetaInt, lcommon.MetaText:
			default:
				simple = false
			}
		}
		if simple {
			o := make(obj, len(x.Pairs))
			for i, p := range x.Pairs {
				o[i] = kv{fmt.Sprint(metadatum(p.Key)), metadatum(p.Value)}
			}
			return o
		}
		out := make([]any, len(x.Pairs))
		for i, p := range x.Pairs {
			out[i] = obj{{"k", metadatum(p.Key)}, {"v", metadatum(p.Value)}}
		}
		return out
	case nil:
		return nil
	default:
		return fmt.Sprintf("unhandled metadatum %T", x)
	}
}

// ---- certificates ----

func certificate(c lcommon.Certificate) CertView {
	switch x := deref(c).(type) {
	case lcommon.StakeRegistrationCertificate:
		return CertView{Type: "stake_registration", StakeCredential: credPtr(x.StakeCredential)}
	case lcommon.StakeDeregistrationCertificate:
		return CertView{Type: "stake_deregistration", StakeCredential: credPtr(x.StakeCredential)}
	case lcommon.StakeDelegationCertificate:
		cv := CertView{Type: "stake_delegation", PoolKeyHash: x.PoolKeyHash.String()}
		if x.StakeCredential != nil {
			cv.StakeCredential = credPtr(*x.StakeCredential)
		}
		return cv
	case lcommon.PoolRegistrationCertificate:
		p := &PoolParamsView{
			Operator: x.Operator.String(), VrfKeyHash: x.VrfKeyHash.String(),
			Pledge: x.Pledge, Cost: x.Cost, Margin: rat(x.Margin.Rat),
			RewardAccountKeyHash: x.RewardAccount.String(), Owners: []string{}, Relays: []RelayView{},
		}
		for _, o := range x.PoolOwners {
			p.Owners = append(p.Owners, o.String())
		}
		for _, r := range x.Relays {
			p.Relays = append(p.Relays, relay(r))
		}
		if x.PoolMetadata != nil {
			p.Metadata = &AnchorView{URL: x.PoolMetadata.Url, DataHash: hx(x.PoolMetadata.Hash)}
		}
		return CertView{Type: "pool_registration", Pool: p}
	case lcommon.PoolRetirementCertificate:
		return CertView{Type: "pool_retirement", PoolKeyHash: x.PoolKeyHash.String(), RetirementEpoch: u64(x.Epoch)}
	case lcommon.GenesisKeyDelegationCertificate:
		return CertView{Type: "genesis_key_delegation", GenesisHash: hx(x.GenesisHash),
			GenesisDelegateHash: hx(x.GenesisDelegateHash), VrfKeyHash: x.VrfKeyHash.String()}
	case lcommon.MoveInstantaneousRewardsCertificate:
		m := &MIRView{Source: map[uint]string{0: "reserves", 1: "treasury"}[x.Reward.Source], ToOtherPot: x.Reward.OtherPot}
		for cred, amt := range x.Reward.Rewards {
			m.Rewards = append(m.Rewards, MIRRewardView{credential(*cred), num(amt)})
		}
		sort.Slice(m.Rewards, func(i, j int) bool {
			return m.Rewards[i].StakeCredential.KeyHash+m.Rewards[i].StakeCredential.ScriptHash <
				m.Rewards[j].StakeCredential.KeyHash+m.Rewards[j].StakeCredential.ScriptHash
		})
		return CertView{Type: "move_instantaneous_rewards", MIR: m}
	case lcommon.RegistrationCertificate:
		return CertView{Type: "registration", StakeCredential: credPtr(x.StakeCredential), Deposit: u64(x.Amount)}
	case lcommon.DeregistrationCertificate:
		return CertView{Type: "deregistration", StakeCredential: credPtr(x.StakeCredential), Refund: u64(x.Amount)}
	case lcommon.VoteDelegationCertificate:
		return CertView{Type: "vote_delegation", StakeCredential: credPtr(x.StakeCredential), Drep: drepView(x.Drep)}
	case lcommon.StakeVoteDelegationCertificate:
		return CertView{Type: "stake_vote_delegation", StakeCredential: credPtr(x.StakeCredential),
			PoolKeyHash: x.PoolKeyHash.String(), Drep: drepView(x.Drep)}
	case lcommon.StakeRegistrationDelegationCertificate:
		return CertView{Type: "stake_registration_delegation", StakeCredential: credPtr(x.StakeCredential),
			PoolKeyHash: x.PoolKeyHash.String(), Deposit: u64(x.Amount)}
	case lcommon.VoteRegistrationDelegationCertificate:
		return CertView{Type: "vote_registration_delegation", StakeCredential: credPtr(x.StakeCredential),
			Drep: drepView(x.Drep), Deposit: u64(x.Amount)}
	case lcommon.StakeVoteRegistrationDelegationCertificate:
		return CertView{Type: "stake_vote_registration_delegation", StakeCredential: credPtr(x.StakeCredential),
			PoolKeyHash: x.PoolKeyHash.String(), Drep: drepView(x.Drep), Deposit: u64(x.Amount)}
	case lcommon.AuthCommitteeHotCertificate:
		return CertView{Type: "auth_committee_hot", ColdCredential: credPtr(x.ColdCredential), HotCredential: credPtr(x.HotCredential)}
	case lcommon.ResignCommitteeColdCertificate:
		return CertView{Type: "resign_committee_cold", ColdCredential: credPtr(x.ColdCredential), Anchor: anchor(x.Anchor)}
	case lcommon.RegistrationDrepCertificate:
		return CertView{Type: "drep_registration", DrepCredential: credPtr(x.DrepCredential), Deposit: u64(x.Amount), Anchor: anchor(x.Anchor)}
	case lcommon.DeregistrationDrepCertificate:
		return CertView{Type: "drep_deregistration", DrepCredential: credPtr(x.DrepCredential), Refund: u64(x.Amount)}
	case lcommon.UpdateDrepCertificate:
		return CertView{Type: "drep_update", DrepCredential: credPtr(x.DrepCredential), Anchor: anchor(x.Anchor)}
	default:
		return CertView{Type: fmt.Sprintf("unknown_%d", c.Type()), Unrecognised: reflectView(x)}
	}
}

func relay(r lcommon.PoolRelay) RelayView {
	rv := RelayView{Type: map[int]string{0: "single_host_addr", 1: "single_host_name", 2: "multi_host_name"}[r.Type]}
	if r.Port != nil {
		rv.Port = *r.Port
	}
	if r.Ipv4 != nil {
		rv.IPv4 = r.Ipv4.String()
	}
	if r.Ipv6 != nil {
		rv.IPv6 = r.Ipv6.String()
	}
	if r.Hostname != nil {
		rv.Hostname = *r.Hostname
	}
	return rv
}

// ---- governance ----

var voterRoleNames = map[uint8]string{
	0: "constitutional_committee_hot_key", 1: "constitutional_committee_hot_script",
	2: "drep_key", 3: "drep_script", 4: "stake_pool",
}

var voteNames = map[uint8]string{0: "no", 1: "yes", 2: "abstain"}

// votes sorts voters and actions: gouroboros holds them in Go maps, so the
// wire order is gone and map order would differ run to run.
func votes(vps lcommon.VotingProcedures) []VoterVotesView {
	var out []VoterVotesView
	for voter, byAction := range vps {
		if voter == nil {
			continue
		}
		vv := VoterVotesView{Voter: VoterView{Role: voterRoleNames[voter.Type], Hash: hx(voter.Hash[:])}}
		for id, p := range byAction {
			vv.Votes = append(vv.Votes, VoteView{Action: govActionID(id), Vote: voteNames[p.Vote], Anchor: anchor(p.Anchor)})
		}
		sort.Slice(vv.Votes, func(i, j int) bool { return vv.Votes[i].Action < vv.Votes[j].Action })
		out = append(out, vv)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Voter.Role != out[j].Voter.Role {
			return out[i].Voter.Role < out[j].Voter.Role
		}
		return out[i].Voter.Hash < out[j].Voter.Hash
	})
	return out
}

func govAction(a lcommon.GovAction) any {
	switch x := deref(a).(type) {
	case conway.ConwayParameterChangeGovAction:
		return GovActionView{Type: "parameter_change", PreviousAction: govActionID(x.ActionId),
			Parameters: reflectView(x.ParamUpdate), PolicyHash: hx(x.PolicyHash)}
	case lcommon.HardForkInitiationGovAction:
		return GovActionView{Type: "hard_fork_initiation", PreviousAction: govActionID(x.ActionId),
			ProtocolVersion: &VersionView{uint64(x.ProtocolVersion.Major), uint64(x.ProtocolVersion.Minor)}}
	case lcommon.TreasuryWithdrawalGovAction:
		gv := GovActionView{Type: "treasury_withdrawals", PolicyHash: hx(x.PolicyHash)}
		for addr, amt := range x.Withdrawals {
			gv.Withdrawals = append(gv.Withdrawals, WithdrawalView{addr.String(), numU(amt)})
		}
		sort.Slice(gv.Withdrawals, func(i, j int) bool { return gv.Withdrawals[i].RewardAccount < gv.Withdrawals[j].RewardAccount })
		return gv
	case lcommon.NoConfidenceGovAction:
		return GovActionView{Type: "no_confidence", PreviousAction: govActionID(x.ActionId)}
	case lcommon.UpdateCommitteeGovAction:
		gv := GovActionView{Type: "update_committee", PreviousAction: govActionID(x.ActionId), Quorum: rat(x.Quorum.Rat)}
		for _, c := range x.Credentials {
			gv.Remove = append(gv.Remove, credential(c))
		}
		for c, e := range x.CredEpochs {
			gv.Add = append(gv.Add, CommitteeAdd{credential(*c), e})
		}
		sort.Slice(gv.Add, func(i, j int) bool {
			return gv.Add[i].Credential.KeyHash+gv.Add[i].Credential.ScriptHash < gv.Add[j].Credential.KeyHash+gv.Add[j].Credential.ScriptHash
		})
		return gv
	case lcommon.NewConstitutionGovAction:
		return GovActionView{Type: "new_constitution", PreviousAction: govActionID(x.ActionId),
			Constitution: anchor(&x.Constitution.Anchor), PolicyHash: hx(x.Constitution.ScriptHash)}
	case lcommon.InfoGovAction:
		return GovActionView{Type: "info"}
	default:
		return GovActionView{Type: fmt.Sprintf("unknown %T", x), Unrecognised: reflectView(x)}
	}
}

// ---- witnesses and auxiliary data ----

var redeemerPurposes = map[lcommon.RedeemerTag]string{
	lcommon.RedeemerTagSpend: "spend", lcommon.RedeemerTagMint: "mint", lcommon.RedeemerTagCert: "certify",
	lcommon.RedeemerTagReward: "reward", lcommon.RedeemerTagVoting: "vote", lcommon.RedeemerTagProposing: "propose",
	lcommon.RedeemerTagGuarding: "guard",
}

func witnesses(w lcommon.TransactionWitnessSet) *WitnessView {
	if w == nil || reflect.ValueOf(w).Kind() == reflect.Pointer && reflect.ValueOf(w).IsNil() {
		return nil
	}
	wv := &WitnessView{
		NativeScripts:   nativeScripts(w.NativeScripts()),
		PlutusV1Scripts: scriptBytes(w.PlutusV1Scripts()),
		PlutusV2Scripts: scriptBytes(w.PlutusV2Scripts()),
		PlutusV3Scripts: scriptBytes(w.PlutusV3Scripts()),
	}
	for _, v := range w.Vkey() {
		wv.VkeyWitnesses = append(wv.VkeyWitnesses, VkeyWitnessView{hx(v.Vkey), hx(v.Signature)})
	}
	for _, b := range w.Bootstrap() {
		wv.BootstrapWitnesses = append(wv.BootstrapWitnesses, BootstrapWitnessView{hx(b.PublicKey), hx(b.Signature), hx(b.ChainCode), hx(b.Attributes)})
	}
	for _, d := range w.PlutusData() {
		wv.PlutusData = append(wv.PlutusData, plutusData(d.Data))
	}
	if r := w.Redeemers(); r != nil && !(reflect.ValueOf(r).Kind() == reflect.Pointer && reflect.ValueOf(r).IsNil()) {
		for k, v := range r.Iter() {
			p, ok := redeemerPurposes[k.Tag]
			if !ok {
				p = fmt.Sprintf("unknown_%d", k.Tag)
			}
			wv.Redeemers = append(wv.Redeemers, RedeemerView{p, k.Index, plutusData(v.Data.Data), ExUnitsView{v.ExUnits.Memory, v.ExUnits.Steps}})
		}
	}
	return wv
}

func auxData(a lcommon.AuxiliaryData) (*AuxView, error) {
	if a == nil || reflect.ValueOf(a).Kind() == reflect.Pointer && reflect.ValueOf(a).IsNil() {
		return nil, nil
	}
	av := &AuxView{}
	md, err := a.Metadata()
	if err != nil {
		return nil, fmt.Errorf("metadata: %w", err)
	}
	if md != nil {
		av.Metadata = metadatum(md)
	}
	if ns, err := a.NativeScripts(); err == nil {
		av.NativeScripts = nativeScripts(ns)
	}
	if s, err := a.PlutusV1Scripts(); err == nil {
		av.PlutusV1Scripts = scriptBytes(s)
	}
	if s, err := a.PlutusV2Scripts(); err == nil {
		av.PlutusV2Scripts = scriptBytes(s)
	}
	if s, err := a.PlutusV3Scripts(); err == nil {
		av.PlutusV3Scripts = scriptBytes(s)
	}
	return av, nil
}

// ---- header ----

// header reads fields by name from the concrete era header, because the
// BlockHeader interface stops at hash, slot and size; Shelley..Alonzo and
// Babbage+ name their VRF and operational-certificate fields differently.
func header(h ledger.BlockHeader) HeaderView {
	hash, prev, body := h.Hash(), h.PrevHash(), h.BlockBodyHash()
	iv := h.IssuerVkey()
	hv := HeaderView{
		Hash: hash.String(), BlockNumber: h.BlockNumber(), Slot: h.SlotNumber(),
		IssuerVkey: hx(iv[:]), BlockBodySize: h.BlockBodySize(), BlockBodyHash: body.String(),
	}
	// The first block after genesis has a null previous hash on the wire.
	if prev != (lcommon.Blake2b256{}) {
		hv.PrevHash = prev.String()
	}
	rv := reflect.Indirect(reflect.ValueOf(h))
	if rv.Kind() != reflect.Struct {
		return hv
	}
	if sig := rv.FieldByName("Signature"); sig.IsValid() && sig.Kind() == reflect.Slice {
		hv.Signature = hx(sig.Bytes())
	}
	b := rv.FieldByName("Body")
	if !b.IsValid() || b.Kind() != reflect.Struct {
		return hv
	}
	field := func(name string) (reflect.Value, bool) {
		f := b.FieldByName(name)
		return f, f.IsValid()
	}
	vrf := func(name string) *VrfView {
		if f, ok := field(name); ok {
			if r, ok := f.Interface().(lcommon.VrfResult); ok {
				return &VrfView{hx(r.Output), hx(r.Proof)}
			}
		}
		return nil
	}
	if f, ok := field("VrfKey"); ok {
		hv.VrfVkey = hx(f.Bytes())
	}
	hv.VrfResult, hv.NonceVrf, hv.LeaderVrf = vrf("VrfResult"), vrf("NonceVrf"), vrf("LeaderVrf")
	if f, ok := field("OpCert"); ok { // Babbage+
		hv.OperationalCert = &OpCertView{
			HotVkey: hx(f.FieldByName("HotVkey").Bytes()), SequenceNumber: f.FieldByName("SequenceNumber").Uint(),
			KesPeriod: f.FieldByName("KesPeriod").Uint(), Signature: hx(f.FieldByName("Signature").Bytes()),
		}
	} else if f, ok := field("OpCertHotVkey"); ok { // Shelley..Alonzo
		hv.OperationalCert = &OpCertView{
			HotVkey: hx(f.Bytes()), SequenceNumber: b.FieldByName("OpCertSequenceNumber").Uint(),
			KesPeriod: b.FieldByName("OpCertKesPeriod").Uint(), Signature: hx(b.FieldByName("OpCertSignature").Bytes()),
		}
	}
	if f, ok := field("ProtoVersion"); ok {
		hv.ProtocolVersion = &VersionView{f.FieldByName("Major").Uint(), f.FieldByName("Minor").Uint()}
	} else if f, ok := field("ProtoMajorVersion"); ok {
		hv.ProtocolVersion = &VersionView{f.Uint(), b.FieldByName("ProtoMinorVersion").Uint()}
	}
	return hv
}

// ---- block ----

func buildBlock(lb ledger.Block) (BlockView, error) {
	bv := BlockView{Era: lb.Era().Name, Header: header(lb.Header()), Transactions: []TxView{}}
	for i, tx := range lb.Transactions() {
		id := tx.Hash()
		tv := TxView{
			Index: i, ID: id.String(), Valid: tx.IsValid(),
			Inputs: inputs(tx.Inputs()), ReferenceInputs: inputs(tx.ReferenceInputs()),
			CollateralInputs: inputs(tx.Collateral()),
			Fee:              num(tx.Fee()), TTL: tx.TTL(), ValidityIntervalStart: tx.ValidityIntervalStart(),
			CurrentTreasuryValue: num(tx.CurrentTreasuryValue()), Donation: num(tx.Donation()),
			Witnesses: witnesses(tx.Witnesses()),
		}
		if tv.Inputs == nil {
			tv.Inputs = []string{}
		}
		if tc := tx.TotalCollateral(); tc != nil && tc.Sign() != 0 {
			tv.TotalCollateral = num(tc)
		}
		if tv.Donation == "0" {
			tv.Donation = ""
		}
		if tv.CurrentTreasuryValue == "0" {
			tv.CurrentTreasuryValue = ""
		}
		tv.Outputs = []OutputView{}
		for _, o := range tx.Outputs() {
			tv.Outputs = append(tv.Outputs, output(o))
		}
		if cr := tx.CollateralReturn(); cr != nil && !(reflect.ValueOf(cr).Kind() == reflect.Pointer && reflect.ValueOf(cr).IsNil()) {
			o := output(cr)
			tv.CollateralReturn = &o
		}
		for _, c := range tx.Certificates() {
			tv.Certificates = append(tv.Certificates, certificate(c))
		}
		for addr, amt := range tx.Withdrawals() {
			tv.Withdrawals = append(tv.Withdrawals, WithdrawalView{addr.String(), num(amt)})
		}
		sort.Slice(tv.Withdrawals, func(i, j int) bool { return tv.Withdrawals[i].RewardAccount < tv.Withdrawals[j].RewardAccount })
		if m := tx.AssetMint(); m != nil {
			tv.Mint = assetList(m, m.Asset)
		}
		for _, s := range tx.RequiredSigners() {
			tv.RequiredSigners = append(tv.RequiredSigners, s.String())
		}
		if h := tx.AuxDataHash(); h != nil {
			tv.AuxiliaryDataHash = h.String()
		}
		tv.NetworkID = networkID(tx)
		if h := tx.ScriptDataHash(); h != nil {
			tv.ScriptDataHash = h.String()
		}
		if epoch, ups := tx.ProtocolParameterUpdates(); len(ups) > 0 {
			pv := &PPUpdateView{Epoch: epoch}
			for g, u := range ups {
				pv.Proposals = append(pv.Proposals, PPProposalView{g.String(), reflectView(u)})
			}
			sort.Slice(pv.Proposals, func(i, j int) bool { return pv.Proposals[i].GenesisHash < pv.Proposals[j].GenesisHash })
			tv.ProtocolParameterUpdates = pv
		}
		tv.Votes = votes(tx.VotingProcedures())
		for _, p := range tx.ProposalProcedures() {
			a := p.Anchor()
			tv.Proposals = append(tv.Proposals, ProposalView{
				Deposit: p.Deposit(), ReturnAddress: p.RewardAccount().String(),
				Action: govAction(p.GovAction()), Anchor: *anchor(&a),
			})
		}
		av, err := auxData(tx.AuxiliaryData())
		if err != nil {
			return bv, fmt.Errorf("tx %d auxiliary data: %w", i, err)
		}
		tv.AuxiliaryData = av
		bv.Transactions = append(bv.Transactions, tv)
	}
	return bv, nil
}

// ---- reflection fallback ----

// reflectView renders a gouroboros value with no hand-written view
// (protocol parameter updates, mostly): exported fields in snake_case, nil
// pointers dropped, bytes as hex, rationals as "n/d", maps as key/value
// lists. cbor helper embeds (StructAsArray, DecodeStoreCbor) are skipped.
func reflectView(v any) any {
	return reflectValue(reflect.ValueOf(v), 0)
}

var (
	ratType    = reflect.TypeOf(cbor.Rat{})
	bigIntType = reflect.TypeOf(big.Int{})
	stringer   = reflect.TypeOf((*fmt.Stringer)(nil)).Elem()
)

func reflectValue(rv reflect.Value, depth int) any {
	if !rv.IsValid() || depth > 64 {
		return nil
	}
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	switch rv.Type() {
	case ratType:
		r := rv.Interface().(cbor.Rat)
		return rat(r.Rat)
	case bigIntType:
		b := rv.Interface().(big.Int)
		return num(&b)
	}
	if rv.Type().PkgPath() != "" && rv.Type().Implements(stringer) && rv.Kind() != reflect.Struct {
		return rv.Interface().(fmt.Stringer).String()
	}
	switch rv.Kind() {
	case reflect.Struct:
		o := obj{}
		t := rv.Type()
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			if !sf.IsExported() || (sf.Anonymous && sf.Type.PkgPath() == "github.com/blinklabs-io/gouroboros/cbor") {
				continue
			}
			val := reflectValue(rv.Field(i), depth+1)
			if val == nil {
				continue
			}
			if sf.Anonymous {
				if inner, ok := val.(obj); ok {
					o = append(o, inner...)
					continue
				}
			}
			o = append(o, kv{snake(sf.Name), val})
		}
		return o
	case reflect.Slice, reflect.Array:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			b := make([]byte, rv.Len())
			reflect.Copy(reflect.ValueOf(b), rv)
			return hx(b)
		}
		out := make([]any, rv.Len())
		for i := range out {
			out[i] = reflectValue(rv.Index(i), depth+1)
		}
		return out
	case reflect.Map:
		type pair struct {
			k   string
			out any
		}
		var ps []pair
		for _, k := range rv.MapKeys() {
			kj := reflectValue(k, depth+1)
			ks, _ := json.Marshal(kj)
			ps = append(ps, pair{string(ks), obj{{"key", kj}, {"value", reflectValue(rv.MapIndex(k), depth+1)}}})
		}
		sort.Slice(ps, func(i, j int) bool { return ps[i].k < ps[j].k })
		out := make([]any, len(ps))
		for i, p := range ps {
			out[i] = p.out
		}
		return out
	default:
		return rv.Interface()
	}
}

func snake(s string) string {
	var b strings.Builder
	r := []rune(s)
	for i, c := range r {
		if unicode.IsUpper(c) {
			if i > 0 && (unicode.IsLower(r[i-1]) || unicode.IsDigit(r[i-1]) || (i+1 < len(r) && unicode.IsLower(r[i+1]))) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(c))
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// networkID reads body key 15, which is not on the Transaction interface.
// Conway bodies hold it as TxNetworkId *uint8 (nil when absent); Alonzo and
// Babbage hold NetworkId uint8 plus a presence flag in TransactionBodyBase,
// which is what tells an explicit 0 from an absent field.
func networkID(tx lcommon.Transaction) *uint8 {
	rv := reflect.Indirect(reflect.ValueOf(tx))
	if rv.Kind() != reflect.Struct {
		return nil
	}
	body := rv.FieldByName("Body")
	if !body.IsValid() || body.Kind() != reflect.Struct {
		return nil
	}
	if p := body.FieldByName("TxNetworkId"); p.IsValid() && p.Kind() == reflect.Pointer {
		if p.IsNil() {
			return nil
		}
		n := uint8(p.Elem().Uint())
		return &n
	}
	field := body.FieldByName("NetworkId")
	if !field.IsValid() || field.Kind() != reflect.Uint8 {
		return nil
	}
	ptr := reflect.New(body.Type())
	ptr.Elem().Set(body)
	present, ok := ptr.Interface().(interface{ NetworkIdPresent() bool })
	n := uint8(field.Uint())
	if (ok && present.NetworkIdPresent()) || n != 0 {
		return &n
	}
	return nil
}
