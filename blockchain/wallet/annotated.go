package wallet

import (
	"encoding/json"
	"fmt"

	log "github.com/sirupsen/logrus"
	"github.com/tendermint/tmlibs/db"

	"github.com/bytom/blockchain/account"
	"github.com/bytom/blockchain/asset"
	"github.com/bytom/blockchain/query"
	"github.com/bytom/blockchain/signers"
	"github.com/bytom/common"
	"github.com/bytom/consensus"
	"github.com/bytom/crypto/sha3pool"
	"github.com/bytom/errors"
	"github.com/bytom/protocol/bc"
	"github.com/bytom/protocol/bc/legacy"
	"github.com/bytom/protocol/vm/vmutil"
)

// annotateTxs adds asset data to transactions
func annotateTxsAsset(w *Wallet, txs []*query.AnnotatedTx) {
	for i, tx := range txs {
		for j, input := range tx.Inputs {
			alias, definition, err := w.getAliasDefinition(input.AssetID)
			if err != nil {
				continue
			}
			txs[i].Inputs[j].AssetAlias = alias
			txs[i].Inputs[j].AssetDefinition = &definition
		}
		for j, output := range tx.Outputs {
			alias, definition, err := w.getAliasDefinition(output.AssetID)
			if err != nil {
				continue
			}
			txs[i].Outputs[j].AssetAlias = alias
			txs[i].Outputs[j].AssetDefinition = &definition
		}
	}
}

func (w *Wallet) getExternalDefinition(assetID *bc.AssetID) (json.RawMessage, error) {

	definitionByte := w.DB.Get(asset.CalcExtAssetKey(assetID))
	if definitionByte == nil {
		return nil, nil
	}

	definitionMap := make(map[string]interface{})
	if err := json.Unmarshal(definitionByte, &definitionMap); err != nil {
		return nil, err
	}

	saveAlias := assetID.String()
	storeBatch := w.DB.NewBatch()

	externalAsset := &asset.Asset{AssetID: *assetID, Alias: &saveAlias, DefinitionMap: definitionMap, Signer: &signers.Signer{Type: "external"}}
	if rawAsset, err := json.Marshal(externalAsset); err == nil {
		log.WithFields(log.Fields{"assetID": assetID.String(), "alias": saveAlias}).Info("index external asset")
		storeBatch.Set(asset.Key(assetID), rawAsset)
	}
	storeBatch.Set(asset.AliasKey(saveAlias), []byte(assetID.String()))
	storeBatch.Write()

	return definitionByte, nil

}

func (w *Wallet) getAliasDefinition(assetID bc.AssetID) (string, json.RawMessage, error) {
	//btm
	if assetID.String() == consensus.BTMAssetID.String() {
		alias := consensus.BTMAlias
		definition := []byte(asset.DefaultNativeAsset.RawDefinitionByte)

		return alias, definition, nil
	}

	//local asset and saved external asset
	localAsset, err := w.AssetReg.FindByID(nil, &assetID)
	if err != nil {
		return "", nil, err
	}
	alias := *localAsset.Alias
	definition := []byte(localAsset.RawDefinitionByte)
	return alias, definition, nil

	//external asset
	if definition, err := w.getExternalDefinition(&assetID); definition != nil {
		return assetID.String(), definition, err
	}

	return "", nil, fmt.Errorf("look up asset %s :not found ", assetID.String())
}

// annotateTxs adds account data to transactions
func annotateTxsAccount(txs []*query.AnnotatedTx, walletDB db.DB) {
	for i, tx := range txs {
		for j, input := range tx.Inputs {
			//issue asset tx input SpentOutputID is nil
			if input.SpentOutputID == nil {
				continue
			}
			localAccount, err := getAccountFromUTXO(*input.SpentOutputID, walletDB)
			if localAccount == nil || err != nil {
				continue
			}
			txs[i].Inputs[j].AccountAlias = localAccount.Alias
			txs[i].Inputs[j].AccountID = localAccount.ID
		}
		for j, output := range tx.Outputs {
			localAccount, err := getAccountFromACP(output.ControlProgram, walletDB)
			if localAccount == nil || err != nil {
				continue
			}
			txs[i].Outputs[j].AccountAlias = localAccount.Alias
			txs[i].Outputs[j].AccountID = localAccount.ID
		}
	}
}

func getAccountFromUTXO(outputID bc.Hash, walletDB db.DB) (*account.Account, error) {
	accountUTXO := account.UTXO{}
	localAccount := account.Account{}

	accountUTXOValue := walletDB.Get(account.UTXOKey(outputID))
	if accountUTXOValue == nil {
		return nil, errors.Wrap(fmt.Errorf("failed get account utxo:%x ", outputID))
	}

	if err := json.Unmarshal(accountUTXOValue, &accountUTXO); err != nil {
		return nil, errors.Wrap(err)
	}

	accountValue := walletDB.Get(account.Key(accountUTXO.AccountID))
	if accountValue == nil {
		return nil, errors.Wrap(fmt.Errorf("failed get account:%s ", accountUTXO.AccountID))
	}
	if err := json.Unmarshal(accountValue, &localAccount); err != nil {
		return nil, errors.Wrap(err)
	}

	return &localAccount, nil
}

func getAccountFromACP(program []byte, walletDB db.DB) (*account.Account, error) {
	var hash common.Hash
	accountCP := account.CtrlProgram{}
	localAccount := account.Account{}

	sha3pool.Sum256(hash[:], program)

	rawProgram := walletDB.Get(account.CPKey(hash))
	if rawProgram == nil {
		return nil, errors.Wrap(fmt.Errorf("failed get account control program:%x ", hash))
	}

	if err := json.Unmarshal(rawProgram, &accountCP); err != nil {
		return nil, errors.Wrap(err)
	}

	accountValue := walletDB.Get(account.Key(accountCP.AccountID))
	if accountValue == nil {
		return nil, errors.Wrap(fmt.Errorf("failed get account:%s ", accountCP.AccountID))
	}

	if err := json.Unmarshal(accountValue, &localAccount); err != nil {
		return nil, errors.Wrap(err)
	}

	return &localAccount, nil
}

var emptyJSONObject = json.RawMessage(`{}`)

func isValidJSON(b []byte) bool {
	var v interface{}
	err := json.Unmarshal(b, &v)
	return err == nil
}

func buildAnnotatedTransaction(orig *legacy.Tx, b *legacy.Block, indexInBlock uint32) *query.AnnotatedTx {
	tx := &query.AnnotatedTx{
		ID:                     orig.ID,
		Timestamp:              b.Time(),
		BlockID:                b.Hash(),
		BlockHeight:            b.Height,
		Position:               indexInBlock,
		BlockTransactionsCount: uint32(len(b.Transactions)),
		ReferenceData:          &emptyJSONObject,
		Inputs:                 make([]*query.AnnotatedInput, 0, len(orig.Inputs)),
		Outputs:                make([]*query.AnnotatedOutput, 0, len(orig.Outputs)),
	}
	if isValidJSON(orig.ReferenceData) {
		referenceData := json.RawMessage(orig.ReferenceData)
		tx.ReferenceData = &referenceData
	}
	for i := range orig.Inputs {
		tx.Inputs = append(tx.Inputs, buildAnnotatedInput(orig, uint32(i)))
	}
	for i := range orig.Outputs {
		tx.Outputs = append(tx.Outputs, buildAnnotatedOutput(orig, i))
	}
	return tx
}

func buildAnnotatedInput(tx *legacy.Tx, i uint32) *query.AnnotatedInput {
	orig := tx.Inputs[i]
	in := &query.AnnotatedInput{
		AssetDefinition: &emptyJSONObject,
		ReferenceData:   &emptyJSONObject,
	}
	if !orig.IsCoinbase() {
		in.AssetID = orig.AssetID()
		in.Amount = orig.Amount()
	}
	if isValidJSON(orig.ReferenceData) {
		referenceData := json.RawMessage(orig.ReferenceData)
		in.ReferenceData = &referenceData
	}

	id := tx.Tx.InputIDs[i]
	e := tx.Entries[id]
	switch e := e.(type) {
	case *bc.Spend:
		in.Type = "spend"
		in.ControlProgram = orig.ControlProgram()
		in.SpentOutputID = e.SpentOutputId
	case *bc.Issuance:
		in.Type = "issue"
		in.IssuanceProgram = orig.IssuanceProgram()
	case *bc.Coinbase:
		in.Type = "coinbase"
		in.Arbitrary = e.Arbitrary
	}
	return in
}

func buildAnnotatedOutput(tx *legacy.Tx, idx int) *query.AnnotatedOutput {
	orig := tx.Outputs[idx]
	outid := tx.OutputID(idx)
	out := &query.AnnotatedOutput{
		OutputID:        *outid,
		Position:        idx,
		AssetID:         *orig.AssetId,
		AssetDefinition: &emptyJSONObject,
		Amount:          orig.Amount,
		ControlProgram:  orig.ControlProgram,
		ReferenceData:   &emptyJSONObject,
	}
	if isValidJSON(orig.ReferenceData) {
		referenceData := json.RawMessage(orig.ReferenceData)
		out.ReferenceData = &referenceData
	}
	if vmutil.IsUnspendable(out.ControlProgram) {
		out.Type = "retire"
	} else {
		out.Type = "control"
	}
	return out
}
