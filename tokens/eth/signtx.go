package eth

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fsn-dev/crossChain-Bridge/common"
	"github.com/fsn-dev/crossChain-Bridge/dcrm"
	"github.com/fsn-dev/crossChain-Bridge/log"
	"github.com/fsn-dev/crossChain-Bridge/tokens"
	"github.com/fsn-dev/crossChain-Bridge/tools/crypto"
	"github.com/fsn-dev/crossChain-Bridge/types"
)

var (
	retryCount    = 15
	retryInterval = 10 * time.Second
	waitInterval  = 10 * time.Second
)

// DcrmSignTransaction dcrm sign raw tx
func (b *Bridge) DcrmSignTransaction(rawTx interface{}, args *tokens.BuildTxArgs) (signTx interface{}, txHash string, err error) {
	swapinNonce--
	tx, ok := rawTx.(*types.Transaction)
	if !ok {
		return nil, "", errors.New("wrong raw tx param")
	}
	signer := b.Signer
	msgHash := signer.Hash(tx)
	jsondata, _ := json.Marshal(args)
	msgContext := string(jsondata)
	keyID, err := dcrm.DoSignOne(msgHash.String(), msgContext)
	if err != nil {
		return nil, "", err
	}
	log.Info(b.TokenConfig.BlockChain+" DcrmSignTransaction start", "keyID", keyID, "msghash", msgHash.String(), "txid", args.SwapID)
	time.Sleep(waitInterval)

	var rsv string
	i := 0
	for ; i < retryCount; i++ {
		signStatus, err2 := dcrm.GetSignStatus(keyID)
		if err2 == nil {
			if len(signStatus.Rsv) != 1 {
				return nil, "", fmt.Errorf("get sign status require one rsv but have %v (keyID = %v)", len(signStatus.Rsv), keyID)
			}

			rsv = signStatus.Rsv[0]
			break
		}
		switch err2 {
		case dcrm.ErrGetSignStatusFailed, dcrm.ErrGetSignStatusTimeout:
			return nil, "", err2
		}
		log.Warn("retry get sign status as error", "err", err2, "txid", args.SwapID)
		time.Sleep(retryInterval)
	}
	if i == retryCount || rsv == "" {
		return nil, "", errors.New("get sign status failed")
	}

	log.Trace(b.TokenConfig.BlockChain+" DcrmSignTransaction get rsv success", "keyID", keyID, "rsv", rsv)

	signature := common.FromHex(rsv)

	if len(signature) != crypto.SignatureLength {
		log.Error("DcrmSignTransaction wrong length of signature")
		return nil, "", errors.New("wrong signature of keyID " + keyID)
	}

	signedTx, err := tx.WithSignature(signer, signature)
	if err != nil {
		return nil, "", err
	}

	sender, err := types.Sender(signer, signedTx)
	if err != nil {
		return nil, "", err
	}

	token := b.TokenConfig
	if sender.String() != token.DcrmAddress {
		log.Error("DcrmSignTransaction verify sender failed", "have", sender.String(), "want", token.DcrmAddress)
		return nil, "", errors.New("wrong sender address")
	}
	txHash = signedTx.Hash().String()
	swapinNonce++
	log.Info(b.TokenConfig.BlockChain+" DcrmSignTransaction success", "keyID", keyID, "txhash", txHash, "nonce", swapinNonce)
	return signedTx, txHash, err
}
