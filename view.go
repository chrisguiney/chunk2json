package main

import "encoding/json"

// The view types are the human-readable object graph emitted by default.
// They are filled from gouroboros's decoded ledger types (see build.go), so
// they show what gouroboros exposes: wire order, unknown fields and
// encoding choices are not represented. Use -wire for the CDDL-labelled
// raw CBOR when those matter.
//
// Amounts that can exceed 2^53 are json.Number so they print exactly.
//
// gouroboros normalises some on-chain data, and these views inherit it:
// zero-quantity assets in outputs and mints are dropped, and duplicate
// redeemer keys are merged. -wire shows them.

type BlockView struct {
	Era          string     `json:"era"`
	Header       HeaderView `json:"header"`
	Transactions []TxView   `json:"transactions"`
}

type HeaderView struct {
	Hash            string       `json:"hash"`
	PrevHash        string       `json:"prev_hash,omitempty"` // empty for the block after genesis
	BlockNumber     uint64       `json:"block_number"`
	Slot            uint64       `json:"slot"`
	IssuerVkey      string       `json:"issuer_vkey"`
	VrfVkey         string       `json:"vrf_vkey,omitempty"`
	VrfResult       *VrfView     `json:"vrf_result,omitempty"` // Babbage+
	NonceVrf        *VrfView     `json:"nonce_vrf,omitempty"`  // Shelley..Alonzo
	LeaderVrf       *VrfView     `json:"leader_vrf,omitempty"` // Shelley..Alonzo
	BlockBodySize   uint64       `json:"block_body_size"`
	BlockBodyHash   string       `json:"block_body_hash"`
	OperationalCert *OpCertView  `json:"operational_cert,omitempty"`
	ProtocolVersion *VersionView `json:"protocol_version,omitempty"`
	Signature       string       `json:"signature,omitempty"`
}

type VrfView struct {
	Output string `json:"output"`
	Proof  string `json:"proof"`
}

type OpCertView struct {
	HotVkey        string `json:"hot_vkey"`
	SequenceNumber uint64 `json:"sequence_number"`
	KesPeriod      uint64 `json:"kes_period"`
	Signature      string `json:"signature"`
}

type VersionView struct {
	Major uint64 `json:"major"`
	Minor uint64 `json:"minor"`
}

type TxView struct {
	Index                    int              `json:"index"`
	ID                       string           `json:"id"`
	Valid                    bool             `json:"valid"`
	Inputs                   []string         `json:"inputs"`
	ReferenceInputs          []string         `json:"reference_inputs,omitempty"`
	CollateralInputs         []string         `json:"collateral_inputs,omitempty"`
	Outputs                  []OutputView     `json:"outputs"`
	CollateralReturn         *OutputView      `json:"collateral_return,omitempty"`
	TotalCollateral          json.Number      `json:"total_collateral,omitempty"`
	Fee                      json.Number      `json:"fee"`
	TTL                      uint64           `json:"ttl,omitempty"`
	ValidityIntervalStart    uint64           `json:"validity_interval_start,omitempty"`
	Certificates             []CertView       `json:"certificates,omitempty"`
	Withdrawals              []WithdrawalView `json:"withdrawals,omitempty"`
	Mint                     []AssetView      `json:"mint,omitempty"`
	RequiredSigners          []string         `json:"required_signers,omitempty"`
	AuxiliaryDataHash        string           `json:"auxiliary_data_hash,omitempty"`
	NetworkID                *uint8           `json:"network_id,omitempty"`
	ScriptDataHash           string           `json:"script_data_hash,omitempty"`
	ProtocolParameterUpdates *PPUpdateView    `json:"protocol_parameter_updates,omitempty"`
	Votes                    []VoterVotesView `json:"votes,omitempty"`
	Proposals                []ProposalView   `json:"proposals,omitempty"`
	CurrentTreasuryValue     json.Number      `json:"current_treasury_value,omitempty"`
	Donation                 json.Number      `json:"donation,omitempty"`
	Witnesses                *WitnessView     `json:"witnesses,omitempty"`
	AuxiliaryData            *AuxView         `json:"auxiliary_data,omitempty"`
}

type OutputView struct {
	Address     string      `json:"address"`
	Lovelace    json.Number `json:"lovelace"`
	Assets      []AssetView `json:"assets,omitempty"`
	DatumHash   string      `json:"datum_hash,omitempty"`
	InlineDatum any         `json:"inline_datum,omitempty"`
	ScriptRef   *ScriptView `json:"script_ref,omitempty"`
}

type AssetView struct {
	PolicyID     string      `json:"policy_id"`
	AssetName    string      `json:"asset_name,omitempty"` // only when printable UTF-8
	AssetNameHex string      `json:"asset_name_hex"`
	Quantity     json.Number `json:"quantity"`
}

type WithdrawalView struct {
	RewardAccount string      `json:"reward_account"`
	Amount        json.Number `json:"amount"`
}

type CredentialView struct {
	KeyHash    string `json:"key_hash,omitempty"`
	ScriptHash string `json:"script_hash,omitempty"`
}

type DrepView struct {
	Type       string `json:"type"` // key_hash, script_hash, always_abstain, always_no_confidence
	KeyHash    string `json:"key_hash,omitempty"`
	ScriptHash string `json:"script_hash,omitempty"`
}

type AnchorView struct {
	URL      string `json:"url"`
	DataHash string `json:"data_hash"`
}

// CertView carries every certificate kind; Type says which, and only that
// kind's fields are set.
type CertView struct {
	Type                string          `json:"type"`
	StakeCredential     *CredentialView `json:"stake_credential,omitempty"`
	PoolKeyHash         string          `json:"pool_key_hash,omitempty"`
	Drep                *DrepView       `json:"drep,omitempty"`
	Deposit             *uint64         `json:"deposit,omitempty"`
	Refund              *uint64         `json:"refund,omitempty"`
	Pool                *PoolParamsView `json:"pool,omitempty"`
	RetirementEpoch     *uint64         `json:"retirement_epoch,omitempty"`
	GenesisHash         string          `json:"genesis_hash,omitempty"`
	GenesisDelegateHash string          `json:"genesis_delegate_hash,omitempty"`
	VrfKeyHash          string          `json:"vrf_key_hash,omitempty"`
	MIR                 *MIRView        `json:"move_instantaneous_rewards,omitempty"`
	ColdCredential      *CredentialView `json:"committee_cold_credential,omitempty"`
	HotCredential       *CredentialView `json:"committee_hot_credential,omitempty"`
	DrepCredential      *CredentialView `json:"drep_credential,omitempty"`
	Anchor              *AnchorView     `json:"anchor,omitempty"`
	Unrecognised        any             `json:"unrecognised,omitempty"`
}

type PoolParamsView struct {
	Operator   string `json:"operator"`
	VrfKeyHash string `json:"vrf_key_hash"`
	Pledge     uint64 `json:"pledge"`
	Cost       uint64 `json:"cost"`
	Margin     string `json:"margin"`
	// gouroboros keeps only the reward account's key hash, not its header
	// byte, so this is a hash rather than a stake address.
	RewardAccountKeyHash string      `json:"reward_account_key_hash"`
	Owners               []string    `json:"owners"`
	Relays               []RelayView `json:"relays"`
	Metadata             *AnchorView `json:"metadata,omitempty"`
}

type RelayView struct {
	Type     string  `json:"type"`           // single_host_addr, single_host_name, multi_host_name
	Port     *uint32 `json:"port,omitempty"` // pointer: port 0 is distinct from no port
	IPv4     string  `json:"ipv4,omitempty"`
	IPv6     string  `json:"ipv6,omitempty"`
	Hostname string  `json:"hostname,omitempty"`
}

type MIRView struct {
	Source     string          `json:"source"` // reserves or treasury
	Rewards    []MIRRewardView `json:"rewards,omitempty"`
	ToOtherPot uint64          `json:"to_other_pot,omitempty"`
}

type MIRRewardView struct {
	StakeCredential CredentialView `json:"stake_credential"`
	Amount          json.Number    `json:"amount"`
}

type PPUpdateView struct {
	Epoch     uint64           `json:"epoch"`
	Proposals []PPProposalView `json:"proposals"`
}

type PPProposalView struct {
	GenesisHash string `json:"genesis_hash"`
	Parameters  any    `json:"parameters"`
}

type VoterVotesView struct {
	Voter VoterView  `json:"voter"`
	Votes []VoteView `json:"votes"`
}

type VoterView struct {
	Role string `json:"role"`
	Hash string `json:"hash"`
}

type VoteView struct {
	Action string      `json:"action"` // txid#index
	Vote   string      `json:"vote"`   // yes, no, abstain
	Anchor *AnchorView `json:"anchor,omitempty"`
}

type ProposalView struct {
	Deposit       uint64     `json:"deposit"`
	ReturnAddress string     `json:"return_address"`
	Action        any        `json:"action"`
	Anchor        AnchorView `json:"anchor"`
}

type WitnessView struct {
	VkeyWitnesses      []VkeyWitnessView      `json:"vkey_witnesses,omitempty"`
	BootstrapWitnesses []BootstrapWitnessView `json:"bootstrap_witnesses,omitempty"`
	NativeScripts      []NativeScriptView     `json:"native_scripts,omitempty"`
	PlutusV1Scripts    []string               `json:"plutus_v1_scripts,omitempty"`
	PlutusV2Scripts    []string               `json:"plutus_v2_scripts,omitempty"`
	PlutusV3Scripts    []string               `json:"plutus_v3_scripts,omitempty"`
	PlutusData         []any                  `json:"plutus_data,omitempty"`
	Redeemers          []RedeemerView         `json:"redeemers,omitempty"`
}

type VkeyWitnessView struct {
	Vkey      string `json:"vkey"`
	Signature string `json:"signature"`
}

type BootstrapWitnessView struct {
	PublicKey  string `json:"public_key"`
	Signature  string `json:"signature"`
	ChainCode  string `json:"chain_code"`
	Attributes string `json:"attributes"`
}

type RedeemerView struct {
	Purpose string      `json:"purpose"`
	Index   uint32      `json:"index"`
	Data    any         `json:"data"`
	ExUnits ExUnitsView `json:"ex_units"`
}

type ExUnitsView struct {
	Mem   int64 `json:"mem"`
	Steps int64 `json:"steps"`
}

type NativeScriptView struct {
	Type       string             `json:"type"` // signature, all, any, at_least, invalid_before, invalid_hereafter, require_guard
	KeyHash    string             `json:"key_hash,omitempty"`
	Required   *int64             `json:"required,omitempty"`
	Slot       *uint64            `json:"slot,omitempty"`
	Credential *CredentialView    `json:"credential,omitempty"`
	Scripts    []NativeScriptView `json:"scripts,omitempty"`
}

type ScriptView struct {
	Language string            `json:"language"` // native, plutus_v1, plutus_v2, plutus_v3
	Hash     string            `json:"hash"`
	Native   *NativeScriptView `json:"native,omitempty"`
	Bytes    string            `json:"bytes,omitempty"`
}

type AuxView struct {
	Metadata        any                `json:"metadata,omitempty"`
	NativeScripts   []NativeScriptView `json:"native_scripts,omitempty"`
	PlutusV1Scripts []string           `json:"plutus_v1_scripts,omitempty"`
	PlutusV2Scripts []string           `json:"plutus_v2_scripts,omitempty"`
	PlutusV3Scripts []string           `json:"plutus_v3_scripts,omitempty"`
}
