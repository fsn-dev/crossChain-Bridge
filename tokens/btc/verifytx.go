package btc

import (
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcwallet/wallet/txauthor"
	"github.com/fsn-dev/crossChain-Bridge/common"
	"github.com/fsn-dev/crossChain-Bridge/log"
	"github.com/fsn-dev/crossChain-Bridge/tokens"
	"github.com/fsn-dev/crossChain-Bridge/tokens/btc/electrs"
)

// GetTransaction impl
func (b *Bridge) GetTransaction(txHash string) (interface{}, error) {
	return b.GetTransactionByHash(txHash)
}

// GetTransactionStatus impl
func (b *Bridge) GetTransactionStatus(txHash string) *tokens.TxStatus {
	txStatus := &tokens.TxStatus{}
	elcstStatus, err := b.GetElectTransactionStatus(txHash)
	if err != nil {
		log.Debug(b.TokenConfig.BlockChain+" Bridge::GetElectTransactionStatus fail", "tx", txHash, "err", err)
		return txStatus
	}
	if elcstStatus.BlockHash != nil {
		txStatus.BlockHash = *elcstStatus.BlockHash
	}
	if elcstStatus.BlockTime != nil {
		txStatus.BlockTime = *elcstStatus.BlockTime
	}
	if elcstStatus.BlockHeight != nil {
		txStatus.BlockHeight = *elcstStatus.BlockHeight
		latest, err := b.GetLatestBlockNumber()
		if err != nil {
			log.Debug(b.TokenConfig.BlockChain+" Bridge::GetLatestBlockNumber fail", "err", err)
			return txStatus
		}
		if latest > txStatus.BlockHeight {
			txStatus.Confirmations = latest - txStatus.BlockHeight
		}
	}
	return txStatus
}

// VerifyMsgHash verify msg hash
func (b *Bridge) VerifyMsgHash(rawTx interface{}, msgHash []string, extra interface{}) (err error) {
	authoredTx, ok := rawTx.(*txauthor.AuthoredTx)
	if !ok {
		return tokens.ErrWrongRawTx
	}
	for i, preScript := range authoredTx.PrevScripts {
		sigScript := preScript
		if txscript.IsPayToScriptHash(sigScript) {
			sigScript, err = b.getRedeemScriptByOutputScrpit(preScript)
			if err != nil {
				return err
			}
		}
		sigHash, err := txscript.CalcSignatureHash(sigScript, hashType, authoredTx.Tx, i)
		if err != nil {
			return err
		}
		if hex.EncodeToString(sigHash) != msgHash[i] {
			return tokens.ErrMsgHashMismatch
		}
	}
	return nil
}

// VerifyTransaction impl
func (b *Bridge) VerifyTransaction(txHash string, allowUnstable bool) (*tokens.TxSwapInfo, error) {
	if b.IsSrc {
		return b.verifySwapinTx(txHash, allowUnstable)
	}
	return nil, tokens.ErrBridgeDestinationNotSupported
}

func (b *Bridge) verifySwapinTx(txHash string, allowUnstable bool) (*tokens.TxSwapInfo, error) {
	swapInfo := &tokens.TxSwapInfo{}
	swapInfo.Hash = txHash // Hash
	if !allowUnstable && !b.checkStable(txHash) {
		return swapInfo, tokens.ErrTxNotStable
	}
	tx, err := b.GetTransactionByHash(txHash)
	if err != nil {
		log.Debug(b.TokenConfig.BlockChain+" Bridge::GetTransaction fail", "tx", txHash, "err", err)
		return swapInfo, tokens.ErrTxNotFound
	}
	txStatus := tx.Status
	if txStatus.BlockHeight != nil {
		swapInfo.Height = *txStatus.BlockHeight // Height
	}
	if txStatus.BlockTime != nil {
		swapInfo.Timestamp = *txStatus.BlockTime // Timestamp
	}
	dcrmAddress := b.TokenConfig.DcrmAddress
	value, memoScript, rightReceiver := b.getReceivedValue(tx.Vout, dcrmAddress)
	if !rightReceiver {
		return swapInfo, tokens.ErrTxWithWrongReceiver
	}
	swapInfo.To = dcrmAddress                    // To
	swapInfo.Value = common.BigFromUint64(value) // Value

	bindAddress, bindOk := getBindAddressFromMemoScipt(memoScript)
	swapInfo.Bind = bindAddress // Bind

	swapInfo.From = getTxFrom(tx.Vin) // From

	// check sender
	if swapInfo.From == swapInfo.To {
		return swapInfo, tokens.ErrTxWithWrongSender
	}

	if !tokens.CheckSwapValue(swapInfo.Value, b.IsSrc) {
		return swapInfo, tokens.ErrTxWithWrongValue
	}

	// NOTE: must verify memo at last step (as it can be recall)
	if !bindOk {
		log.Debug("wrong memo", "memo", memoScript)
		return swapInfo, tokens.ErrTxWithWrongMemo
	} else if !tokens.DstBridge.IsValidAddress(swapInfo.Bind) {
		log.Debug("wrong bind address in memo", "bind", swapInfo.Bind)
		return swapInfo, tokens.ErrTxWithWrongMemo
	}

	if !allowUnstable {
		log.Debug("verify swapin pass", "from", swapInfo.From, "to", swapInfo.To, "bind", swapInfo.Bind, "value", swapInfo.Value, "txid", swapInfo.Hash, "height", swapInfo.Height, "timestamp", swapInfo.Timestamp)
	}
	return swapInfo, nil
}

func (b *Bridge) checkStable(txHash string) bool {
	txStatus := b.GetTransactionStatus(txHash)
	return txStatus.BlockHeight > 0 && txStatus.Confirmations >= *b.TokenConfig.Confirmations
}

func (b *Bridge) getReceivedValue(vout []*electrs.ElectTxOut, receiver string) (value uint64, memoScript string, rightReceiver bool) {
	for _, output := range vout {
		switch *output.ScriptpubkeyType {
		case opReturnType:
			memoScript = *output.ScriptpubkeyAsm
			continue
		case p2pkhType:
			if *output.ScriptpubkeyAddress != receiver {
				continue
			}
			rightReceiver = true
			value += *output.Value
		}
	}
	return value, memoScript, rightReceiver
}

func getTxFrom(vin []*electrs.ElectTxin) string {
	for _, input := range vin {
		if input != nil &&
			input.Prevout != nil &&
			input.Prevout.ScriptpubkeyAddress != nil {
			return *input.Prevout.ScriptpubkeyAddress
		}
	}
	return ""
}

func getBindAddressFromMemoScipt(memoScript string) (bind string, ok bool) {
	re := regexp.MustCompile("^OP_RETURN OP_PUSHBYTES_[0-9]* ")
	parts := re.Split(memoScript, -1)
	if len(parts) != 2 {
		return "", false
	}
	memoHex := strings.TrimSpace(parts[1])
	memo := common.FromHex(memoHex)
	if len(memo) <= len(tokens.LockMemoPrefix) {
		return "", false
	}
	if !strings.HasPrefix(string(memo), tokens.LockMemoPrefix) {
		return "", false
	}
	bind = string(memo[len(tokens.LockMemoPrefix):])
	return bind, true
}
