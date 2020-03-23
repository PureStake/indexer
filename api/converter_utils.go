package api

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/algorand/go-algorand-sdk/encoding/msgpack"
	"github.com/algorand/go-algorand-sdk/client/algod/models"
	"github.com/algorand/go-algorand-sdk/crypto"
	sdk_types "github.com/algorand/go-algorand-sdk/types"

	"github.com/algorand/indexer/api/generated"
	"github.com/algorand/indexer/idb"
	"github.com/algorand/indexer/importer"
	"github.com/algorand/indexer/types"
)

//////////////////////////////////////////////////////////////////////
// String decoding helpers (with 'errorArr' helper to group errors) //
//////////////////////////////////////////////////////////////////////

// decodeAddress returns the byte representation of the input string, or appends an error to errorArr
func decodeAddress(str *string, field string, errorArr []string) ([]byte, []string) {
	if str != nil {
		addr, err := sdk_types.DecodeAddress(*str)
		if err != nil {
			return nil, append(errorArr, fmt.Sprintf("Unable to parse address '%s': %v", field, err))
		}
		return addr[:], errorArr
	}
	// Pass through
	return nil, errorArr
}

// decodeAddress converts the role information into a bitmask, or appends an error to errorArr
func decodeAddressRole(role *string, excludeCloseTo *bool, errorArr []string) (uint64, []string) {
	ret := uint64(0)

	// Set sender/receiver bits
	if role != nil {
		lc := strings.ToLower(*role)
		if lc == "sender" {
			ret |= idb.AddressRoleSender|idb.AddressRoleAssetSender
		} else if lc == "receiver" {
			ret |= idb.AddressRoleReceiver|idb.AddressRoleAssetReceiver

			// Also add close to flags to sender unless they were explicitly disabled.
			if excludeCloseTo == nil || !(*excludeCloseTo) {
				ret |= idb.AddressRoleCloseRemainderTo|idb.AddressRoleAssetCloseTo
			}
		} else if lc == "freeze-target" {
			ret |= idb.AddressRoleFreeze
		} else {
			return 0, append(errorArr, fmt.Sprintf("unknown address role: '%s' (expected sender, receiver or freeze-target)", lc))
		}
	}

	return ret, errorArr
}

// decodeSigType validates the input string and dereferences it if present, or appends an error to errorArr
func decodeSigType(str *string, errorArr []string) (string, []string) {
	if str != nil {
		sigTypeLc := strings.ToLower(*str)
		if _, ok := sigTypeEnumMap[sigTypeLc]; ok {
			return sigTypeLc, errorArr
		}
		return "", append(errorArr, fmt.Sprintf("invalid sigtype: '%s'", sigTypeLc))
	}
	// Pass through
	return "", errorArr
}

// decodeType validates the input string and dereferences it if present, or appends an error to errorArr
func decodeType(str *string, errorArr []string) (t int, err []string) {
	if str != nil {
		typeLc := strings.ToLower(*str)
		if val, ok := importer.TypeEnumMap[typeLc]; ok {
			return val, errorArr
		}
		return 0, append(errorArr, fmt.Sprintf("invalid transaction type: '%s'", typeLc))
	}
	// Pass through
	return 0, errorArr
}

////////////////////////////////////////////////////
// Helpers to convert to and from generated types //
////////////////////////////////////////////////////

func assetHoldingToAssetHolding(id uint64, holding models.AssetHolding) generated.AssetHolding {
	return generated.AssetHolding{
		AssetId:  id,
		Amount:   holding.Amount,
		Creator:  holding.Creator,
		IsFrozen: boolPtr(holding.Frozen),
	}
}

func assetParamsToAsset(id uint64, params models.AssetParams) generated.Asset {
	return generated.Asset{
		Index: id,
		Params: generated.AssetParams{
			Clawback:      strPtr(params.ClawbackAddr),
			Creator:       params.Creator,
			Decimals:      uint64(params.Decimals),
			DefaultFrozen: boolPtr(params.DefaultFrozen),
			Freeze:        strPtr(params.FreezeAddr),
			Manager:       strPtr(params.ManagerAddr),
			MetadataHash:  bytePtr(params.MetadataHash),
			Name:          strPtr(params.AssetName),
			Reserve:       strPtr(params.ReserveAddr),
			Total:         params.Total,
			UnitName:      strPtr(params.UnitName),
			Url:           strPtr(params.URL),
		},
	}
}

func accountToAccount(account models.Account) generated.Account {
	// TODO: This data is missing.
	var participation = generated.AccountParticipation{
		SelectionParticipationKey: nil,
		VoteFirstValid:            uint64Ptr(0),
		VoteLastValid:             uint64Ptr(0),
		VoteKeyDilution:           uint64Ptr(0),
		VoteParticipationKey:      nil,
	}

	var assets = make([]generated.AssetHolding, 0)
	for k, v := range account.Assets {
		assets = append(assets, assetHoldingToAssetHolding(k, v))
	}

	var createdAssets = make([]generated.Asset, 0)
	for k, v := range account.AssetParams {
		createdAssets = append(createdAssets, assetParamsToAsset(k, v))
	}

	ret := generated.Account{
		Address:                     account.Address,
		Amount:                      account.Amount,
		AmountWithoutPendingRewards: account.AmountWithoutPendingRewards,
		Assets:                      &assets,
		CreatedAssets:               &createdAssets,
		Participation:               &participation,
		PendingRewards:              account.PendingRewards,
		RewardBase:                  uint64Ptr(0),
		Rewards:                     account.Rewards,
		Round:                       account.Round,
		Status:                      account.Status,
		Type:                        strPtr("unknown"), // TODO: how to get this?
	}

	return ret
}

func sigToTransactionSig(sig sdk_types.Signature) *[]byte {
	if sig == (sdk_types.Signature{}) {
		return nil
	}

	tsig := sig[:]
	return &tsig
}

func msigToTransactionMsig(msig sdk_types.MultisigSig) *generated.TransactionSignatureMultisig {
	if msig.Blank() {
		return nil
	}

	subsigs := make([]generated.TransactionSignatureMultisigSubsignature, 0)
	for _, subsig := range msig.Subsigs {
		subsigs = append(subsigs, generated.TransactionSignatureMultisigSubsignature{
			PublicKey: bytePtr(subsig.Key[:]),
			Signature: sigToTransactionSig(subsig.Sig),
		})
	}

	ret := generated.TransactionSignatureMultisig{
		Subsignature: &subsigs,
		Threshold:    uint64Ptr(uint64(msig.Threshold)),
		Version:      uint64Ptr(uint64(msig.Version)),
	}
	return &ret
}

// TODO: Replace with lsig.Blank() when that gets merged into go-algorand-sdk
func isBlank(lsig sdk_types.LogicSig) bool {
	if lsig.Args != nil {
		return false
	}
	if len(lsig.Logic) != 0 {
		return false
	}
	if !lsig.Msig.Blank() {
		return false
	}
	if lsig.Sig != (sdk_types.Signature{}) {
		return false
	}
	return true
}

func lsigToTransactionLsig(lsig sdk_types.LogicSig) *generated.TransactionSignatureLogicsig {
	if isBlank(lsig) {
		return nil
	}

	args := make([]string, 0)
	for _, arg := range lsig.Args {
		args = append(args, base64.StdEncoding.EncodeToString(arg))
	}

	ret := generated.TransactionSignatureLogicsig{
		Args:              &args,
		Logic:             lsig.Logic,
		MultisigSignature: msigToTransactionMsig(lsig.Msig),
		Signature:         sigToTransactionSig(lsig.Sig),
	}

	return &ret
}

func txnRowToTransaction(row idb.TxnRow) (generated.Transaction, error) {
	if row.Error != nil {
		return generated.Transaction{}, row.Error
	}

	var stxn types.SignedTxnWithAD
	err := msgpack.Decode(row.TxnBytes, &stxn)
	if err != nil {
		return generated.Transaction{}, fmt.Errorf("error decoding transaction bytes: %s", err.Error())
	}

	var payment *generated.TransactionPayment
	var keyreg *generated.TransactionKeyreg
	var assetConfig *generated.TransactionAssetConfig
	var assetFreeze *generated.TransactionAssetFreeze
	var assetTransfer *generated.TransactionAssetTransfer

	switch stxn.Txn.Type {
	case sdk_types.PaymentTx:
		p := generated.TransactionPayment{
			CloseAmount:      uint64Ptr(row.Extra.AssetCloseAmount),
			CloseRemainderTo: addrPtr(stxn.Txn.CloseRemainderTo),
			Receiver:         stxn.Txn.Receiver.String(),
		}
		payment = &p
	case sdk_types.KeyRegistrationTx:
		k := generated.TransactionKeyreg{
			NonParticipation:          boolPtr(stxn.Txn.Nonparticipation),
			SelectionParticipationKey: bytePtr(stxn.Txn.SelectionPK[:]),
			VoteFirstValid:            uint64Ptr(uint64(stxn.Txn.VoteFirst)),
			VoteLastValid:             uint64Ptr(uint64(stxn.Txn.VoteLast)),
			VoteKeyDilution:           uint64Ptr(stxn.Txn.VoteKeyDilution),
			VoteParticipationKey:      bytePtr(stxn.Txn.VotePK[:]),
		}
		keyreg = &k
	case sdk_types.AssetConfigTx:
		assetParams := generated.AssetParams{
			Clawback:      addrPtr(stxn.Txn.AssetParams.Clawback),
			Creator:       stxn.Txn.Sender.String(),
			Decimals:      uint64(stxn.Txn.AssetParams.Decimals),
			DefaultFrozen: boolPtr(stxn.Txn.AssetParams.DefaultFrozen),
			Freeze:        addrPtr(stxn.Txn.AssetParams.Freeze),
			Manager:       addrPtr(stxn.Txn.AssetParams.Manager),
			MetadataHash:  bytePtr(stxn.Txn.AssetParams.MetadataHash[:]),
			Name:          strPtr(stxn.Txn.AssetParams.AssetName),
			Reserve:       addrPtr(stxn.Txn.AssetParams.Reserve),
			Total:         stxn.Txn.AssetParams.Total,
			UnitName:      strPtr(stxn.Txn.AssetParams.UnitName),
			Url:           strPtr(stxn.Txn.AssetParams.URL),
		}
		config := generated.TransactionAssetConfig{
			AssetId: nil,
			Params:  &assetParams,
		}
		assetConfig = &config
	case sdk_types.AssetTransferTx:
		t := generated.TransactionAssetTransfer{
			Amount:   stxn.Txn.AssetAmount,
			AssetId:  uint64(stxn.Txn.XferAsset),
			CloseTo:  addrPtr(stxn.Txn.AssetCloseTo),
			Receiver: stxn.Txn.AssetReceiver.String(),
			Sender:   addrPtr(stxn.Txn.AssetSender),
		}
		assetTransfer = &t
	case sdk_types.AssetFreezeTx:
		f := generated.TransactionAssetFreeze{
			Address:         stxn.Txn.FreezeAccount.String(),
			AssetId:         uint64(stxn.Txn.FreezeAsset),
			NewFreezeStatus: stxn.Txn.AssetFrozen,
		}
		assetFreeze = &f
	}

	sig := generated.TransactionSignature{
		Logicsig: lsigToTransactionLsig(stxn.Lsig),
		Multisig: msigToTransactionMsig(stxn.Msig),
		Sig:      sigToTransactionSig(stxn.Sig),
	}

	txn := generated.Transaction{
		AssetConfigTransaction:   assetConfig,
		AssetFreezeTransaction:   assetFreeze,
		AssetTransferTransaction: assetTransfer,
		PaymentTransaction:       payment,
		KeyregTransaction:        keyreg,
		ClosingAmount:            uint64Ptr(uint64(stxn.ClosingAmount)),
		ConfirmedRound:           uint64Ptr(row.Round),
		IntraRoundOffset:         uint64Ptr(uint64(row.Intra)),
		RoundTime:                uint64Ptr(uint64(row.RoundTime.Unix())),
		Fee:                      uint64(stxn.Txn.Fee),
		FirstValid:               uint64(stxn.Txn.FirstValid),
		GenesisHash:              bytePtr(stxn.SignedTxn.Txn.GenesisHash[:]),
		GenesisId:                strPtr(stxn.SignedTxn.Txn.GenesisID),
		Group:                    bytePtr(stxn.Txn.Group[:]),
		LastValid:                uint64(stxn.Txn.LastValid),
		Lease:                    bytePtr(stxn.Txn.Lease[:]),
		Note:                     bytePtr(stxn.Txn.Note[:]),
		Sender:                   stxn.Txn.Sender.String(),
		ReceiverRewards:          uint64Ptr(uint64(stxn.ReceiverRewards)),
		CloseRewards:             uint64Ptr(uint64(stxn.CloseRewards)),
		SenderRewards:            uint64Ptr(uint64(stxn.SenderRewards)),
		Type:                     string(stxn.Txn.Type),
		Signature:                sig,
		CreatedAssetIndex:        nil,                            // TODO: What is this?
		Id:                       crypto.TransactionID(stxn.Txn), // TODO: This needs to come from the DB because of the GenesisHash / GenesisID
		PoolError:                nil,                            // TODO: What is this?
	}

	return txn, nil
}

func assetParamsToAssetQuery(params generated.SearchForAssetsParams) (idb.AssetsQuery, error) {
	creator, errorArr := decodeAddress(params.Creator, "creator", make([]string, 0))
	if len(errorArr) != 0 {
		return idb.AssetsQuery{}, errors.New(errorArr[0])
	}

	var assetGreaterThan uint64 = 0
	if params.Next != nil {
		agt, err := strconv.ParseUint(*params.Next, 10, 64)
		if err != nil {
			return idb.AssetsQuery{}, fmt.Errorf("unable to parse 'next': %v", err)
		}
		assetGreaterThan = agt
	}

	query := idb.AssetsQuery{
		AssetId:            uintOrDefault(params.AssetId),
		AssetIdGreaterThan: assetGreaterThan,
		Creator:            creator,
		Name:				strOrDefault(params.Name),
		Unit:               strOrDefault(params.Unit),
		Query:              "",
		Limit:              uintOrDefault(params.Limit),
	}

	return query, nil
}

// TODO: idb.TransactionFilter missing:
//      * MinAssetAmount
//      * MaxAssetAmount
// TODO: Convert Max/Min to LessThan/GreaterThan
// TODO: Pagination
func transactionParamsToTransactionFilter(params generated.SearchForTransactionsParams) (filter idb.TransactionFilter, err error) {
	var errorArr = make([]string, 0)

	if params.Round != nil && params.MaxRound != nil && *params.Round > *params.MaxRound {
		errorArr = append(errorArr, "invalid parameters: round > max-round")
	}

	if params.Round != nil && params.MinRound != nil && *params.Round < *params.MinRound {
		errorArr = append(errorArr, "invalid parameters: round < min-round")
	}

	// Integer
	filter.MaxRound = uintOrDefault(params.MaxRound)
	filter.MinRound = uintOrDefault(params.MinRound)
	filter.AssetId = uintOrDefault(params.AssetId)
	filter.Limit = uintOrDefault(params.Limit)
	// TODO: Convert Max/Min to LessThan/GreaterThan
	filter.MaxAlgos = uintOrDefaultMod(params.CurrencyLessThan, 1)
	filter.MinAlgos = uintOrDefaultMod(params.CurrencyGreaterThan, -1)
	filter.Round = params.Round
	//filter.Offset = params.Offset

	// String
	filter.AddressRole, errorArr = decodeAddressRole(params.AddressRole, params.ExcludeCloseTo, errorArr)

	// Address
	filter.Address, errorArr = decodeAddress(params.Address, "address", errorArr)

	// Byte array
	if params.NotePrefix != nil {
		filter.NotePrefix = *params.NotePrefix
	}
	if params.TxId != nil {
		filter.Txid = *params.TxId
	}

	// Time
	if params.AfterTime != nil {
		filter.AfterTime = *params.AfterTime
	}
	if params.BeforeTime != nil {
		filter.BeforeTime = *params.BeforeTime
	}

	// Enum
	filter.SigType, errorArr = decodeSigType(params.SigType, errorArr)
	filter.TypeEnum, errorArr = decodeType(params.TxType, errorArr)

	// If there were any errorArr while setting up the TransactionFilter, return now.
	if len(errorArr) > 0 {
		err = errors.New(strings.Join(errorArr, ", "))
	}

	return
}
