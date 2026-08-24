package keeper

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"

	"github.com/hashicorp/go-metrics"

	ethtypes "github.com/ethereum/go-ethereum/core/types"

	cmttypes "github.com/cometbft/cometbft/types"

	"github.com/cosmos/evm/x/vm/types"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/telemetry"
	sdk "github.com/cosmos/cosmos-sdk/types"
	errortypes "github.com/cosmos/cosmos-sdk/types/errors"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
)

var _ types.MsgServer = &Keeper{}

// EthereumTx implements the gRPC MsgServer interface. It receives a transaction which is then
// executed (i.e applied) against the go-ethereum EVM. The provided SDK Context is set to the Keeper
// so that it can implements and call the StateDB methods without receiving it as a function
// parameter.
func (k *Keeper) EthereumTx(goCtx context.Context, msg *types.MsgEthereumTx) (*types.MsgEthereumTxResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	tx := msg.AsTransaction()
	// MsgEthereumTx.Raw unmarshals to a nil transaction when the raw field is
	// empty, and ValidateBasic is part of the same ante path that a nested
	// message skips, so the executor cannot assume it ran. Fail cleanly instead
	// of dereferencing nil below.
	if tx == nil {
		return nil, errorsmod.Wrap(errortypes.ErrInvalidRequest, "invalid transaction: raw ethereum transaction is empty")
	}

	// Verify that msg.From really is the ECDSA signer of the transaction.
	//
	// This is normally established by the EVM ante handler
	// (ante/evm/05_signature_verification.go), but the ante chain only runs over
	// the *top-level* messages of a tx. Any module that unpacks an embedded
	// sdk.Msg and re-dispatches it through the message router - x/authz MsgExec,
	// x/group MsgSubmitProposal/MsgExec, x/gov proposals, a CosmWasm
	// stargate/Any message, ICA - delivers the nested message here with the ante
	// already behind it, so nothing has checked the signature. An attacker could
	// then take any victim-signed transaction off the mempool and have it
	// executed as the victim. Checking here means the executor never trusts an
	// unverified From, whatever route the message took to reach it.
	//
	// Cost on the normal path is ~zero: msg.AsTransaction() hands back the same
	// *ethtypes.Transaction the ante verified, and go-ethereum caches the
	// recovered sender on it per signer, so this is a cache hit rather than a
	// second ECDSA recovery.
	signer := ethtypes.MakeSigner(types.GetEthChainConfig(), big.NewInt(ctx.BlockHeight()), uint64(ctx.BlockTime().Unix())) //#nosec G115 -- int overflow is not a concern here
	if err := msg.VerifySender(signer); err != nil {
		return nil, errorsmod.Wrapf(errortypes.ErrorInvalidSigner, "signature verification failed: %s", err.Error())
	}

	txIndex := k.GetTxIndexTransient(ctx)

	labels := []metrics.Label{
		telemetry.NewLabel("tx_type", fmt.Sprintf("%d", tx.Type())),
	}
	if tx.To() == nil {
		labels = append(labels, telemetry.NewLabel("execution", "create"))
	} else {
		labels = append(labels, telemetry.NewLabel("execution", "call"))
	}

	response, err := k.ApplyTransaction(ctx, msg.AsTransaction())
	if err != nil {
		return nil, errorsmod.Wrap(err, "failed to apply transaction")
	}

	defer func() {
		telemetry.IncrCounterWithLabels(
			[]string{"tx", "msg", "ethereum_tx", "total"},
			1,
			labels,
		)

		if response.GasUsed != 0 {
			telemetry.IncrCounterWithLabels(
				[]string{"tx", "msg", "ethereum_tx", "gas_used", "total"},
				float32(response.GasUsed),
				labels,
			)

			// Observe which users define a gas limit >> gas used. Note, that
			// gas_limit and gas_used are always > 0
			gasLimit := math.LegacyNewDec(int64(tx.Gas()))                        //#nosec G115 -- int overflow is not a concern here -- tx gas is not going to exceed int64 max value
			gasRatio, err := gasLimit.QuoInt64(int64(response.GasUsed)).Float64() //#nosec G115 -- int overflow is not a concern here -- gas used is not going to exceed int64 max value
			if err == nil {
				telemetry.SetGaugeWithLabels(
					[]string{"tx", "msg", "ethereum_tx", "gas_limit", "per", "gas_used"},
					float32(gasRatio),
					labels,
				)
			}
		}
	}()

	attrs := []sdk.Attribute{
		sdk.NewAttribute(sdk.AttributeKeyAmount, tx.Value().String()),
		// add event for ethereum transaction hash format
		sdk.NewAttribute(types.AttributeKeyEthereumTxHash, response.Hash),
		// add event for index of valid ethereum tx
		sdk.NewAttribute(types.AttributeKeyTxIndex, strconv.FormatUint(txIndex, 10)),
		// add event for eth tx gas used, we can't get it from cosmos tx result when it contains multiple eth tx msgs.
		sdk.NewAttribute(types.AttributeKeyTxGasUsed, strconv.FormatUint(response.GasUsed, 10)),
	}

	if len(ctx.TxBytes()) > 0 {
		// add event for CometBFT transaction hash format
		hash := cmttypes.Tx(ctx.TxBytes()).Hash()
		attrs = append(attrs, sdk.NewAttribute(types.AttributeKeyTxHash, hex.EncodeToString(hash)))
	}

	if to := tx.To(); to != nil {
		attrs = append(attrs, sdk.NewAttribute(types.AttributeKeyRecipient, to.Hex()))
	}

	if response.Failed() {
		attrs = append(attrs, sdk.NewAttribute(types.AttributeKeyEthereumTxFailed, response.VmError))
	}

	// emit events
	ctx.EventManager().EmitEvents(sdk.Events{
		sdk.NewEvent(
			types.EventTypeEthereumTx,
			attrs...,
		),
		sdk.NewEvent(
			sdk.EventTypeMessage,
			sdk.NewAttribute(sdk.AttributeKeyModule, types.AttributeValueCategory),
			sdk.NewAttribute(sdk.AttributeKeySender, types.HexAddress(msg.From)),
			sdk.NewAttribute(types.AttributeKeyTxType, fmt.Sprintf("%d", tx.Type())),
		),
	})

	return response, nil
}

// UpdateParams implements the gRPC MsgServer interface. When an UpdateParams
// proposal passes, it updates the module parameters. The update can only be
// performed if the requested authority is the Cosmos SDK governance module
// account.
func (k *Keeper) UpdateParams(goCtx context.Context, req *types.MsgUpdateParams) (*types.MsgUpdateParamsResponse, error) {
	if k.authority.String() != req.Authority {
		return nil, errorsmod.Wrapf(govtypes.ErrInvalidSigner, "invalid authority, expected %s, got %s", k.authority.String(), req.Authority)
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	if err := k.SetParams(ctx, req.Params); err != nil {
		return nil, err
	}

	return &types.MsgUpdateParamsResponse{}, nil
}

// RegisterPreinstalls implements the gRPC MsgServer interface. When a RegisterPreinstalls
// proposal passes, it creates the preinstalls. The registration can only be
// performed if the requested authority is the Cosmos SDK governance module
// account.
func (k *Keeper) RegisterPreinstalls(goCtx context.Context, req *types.MsgRegisterPreinstalls) (*types.
	MsgRegisterPreinstallsResponse, error,
) {
	if k.authority.String() != req.Authority {
		return nil, errorsmod.Wrapf(govtypes.ErrInvalidSigner, "invalid authority, expected %s, got %s", k.authority.String(), req.Authority)
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	if err := k.AddPreinstalls(ctx, req.Preinstalls); err != nil {
		return nil, err
	}

	return &types.MsgRegisterPreinstallsResponse{}, nil
}
