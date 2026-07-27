package keeper

import (
	"encoding/json"
	"math/big"
	"strconv"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/cosmos/evm/server/config"
	evmtrace "github.com/cosmos/evm/trace"
	"github.com/cosmos/evm/x/vm/statedb"
	"github.com/cosmos/evm/x/vm/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	errortypes "github.com/cosmos/cosmos-sdk/types/errors"
)

// CallEVM performs a smart contract method call using given args.
// Note: if you call this from a precompile context, ensure that
// you use the existing stateDB.
func (k Keeper) CallEVM(ctx sdk.Context, stateDB *statedb.StateDB, abi abi.ABI, from, contract common.Address, commit, callFromPrecompile bool, gasCap *big.Int, method string, args ...interface{}) (_ *types.MsgEthereumTxResponse, err error) {
	ctx, span := ctx.StartSpan(tracer, "CallEVM", trace.WithAttributes(
		attribute.String("from", from.Hex()),
		attribute.String("contract", contract.Hex()),
		attribute.String("method", method),
		attribute.Bool("commit", commit),
	))
	defer func() { evmtrace.EndSpanErr(span, err) }()
	data, err := abi.Pack(method, args...)
	if err != nil {
		return nil, errorsmod.Wrap(
			types.ErrABIPack,
			errorsmod.Wrap(err, "failed to create transaction data").Error(),
		)
	}

	resp, err := k.CallEVMWithData(ctx, stateDB, from, &contract, data, commit, callFromPrecompile, gasCap)
	if err != nil {
		return resp, errorsmod.Wrapf(err, "contract call failed: method '%s', contract '%s'", method, contract)
	}
	return resp, nil
}

// CallEVMWithData performs a smart contract method call using contract data.
// Note: if you call this from a precompile context, ensure that
// you use the existing stateDB.
func (k Keeper) CallEVMWithData(ctx sdk.Context, stateDB *statedb.StateDB, from common.Address, contract *common.Address, data []byte, commit bool, callFromPrecompile bool, gasCap *big.Int) (_ *types.MsgEthereumTxResponse, err error) {
	contractAddr := ""
	if contract != nil {
		contractAddr = contract.Hex()
	}
	ctx, span := ctx.StartSpan(tracer, "CallEVMWithData", trace.WithAttributes(
		attribute.String("from", from.Hex()),
		attribute.String("contract", contractAddr),
		attribute.Bool("commit", commit),
		attribute.Int("data_size", len(data)),
	))
	defer func() { evmtrace.EndSpanErr(span, err) }()
	nonce, err := k.accountKeeper.GetSequence(ctx, from.Bytes())
	if err != nil {
		return nil, err
	}

	gasLimit := config.DefaultGasCap
	if gasCap != nil && gasCap.Sign() > 0 {
		if gasCap.BitLen() <= 64 {
			provided := gasCap.Uint64()
			if provided < gasLimit {
				gasLimit = provided
			}
		}
	}

	msg := core.Message{
		From:       from,
		To:         contract,
		Nonce:      nonce,
		Value:      big.NewInt(0),
		GasLimit:   gasLimit,
		GasPrice:   big.NewInt(0),
		GasTipCap:  big.NewInt(0),
		GasFeeCap:  big.NewInt(0),
		Data:       data,
		AccessList: ethtypes.AccessList{},
	}

	// v0.6.0: the StateDB is supplied by the caller and ApplyMessage rejects a nil
	// StateDB (returns ErrNilStateDB). Pass it (and callFromPrecompile) straight
	// through so that contract — and the precompile snapshot/flush chain — is
	// preserved.
	res, err := k.ApplyMessage(ctx, stateDB, msg, nil, commit, callFromPrecompile, true)
	if err != nil {
		return nil, err
	}

	if res.Failed() {
		// push-chain audit fix: unlike upstream we deliberately do NOT call
		// k.ResetGasMeterAndConsumeGas(ctx, ctx.GasMeter().Limit()) here. Consuming
		// the full gas limit on a revert can overflow the parent gas meter. We
		// surface the VM error and leave the gas consumed during execution in
		// place; the EVM has already rolled back the reverted call frame via its
		// own snapshot, so no extra state isolation is required.
		return res, errorsmod.Wrap(types.ErrVMExecution, res.VmError)
	}

	ctx.GasMeter().ConsumeGas(res.GasUsed, "apply evm message")

	return res, nil
}

// NOTE: A DerivedTx is a MsgEthereumTx reconstructed from Tendermint ABCI events.
// These transactions are not submitted via Ethereum RPC but are derived from Cosmos-based messages
// to provide consistent EVM compatibility and traceability.

// DerivedEVMCall performs an internal EVM contract call using the given method and arguments.
// It ABI-encodes the method call, constructs the transaction data, and invokes the EVM.
//
// Returns (msg, err), where:
//   - msg contains the EVM execution result (including revert data if applicable)
//   - err is non-nil if the EVM execution failed or the contract call reverted.
//
// Note: If err != nil and msg != nil and msg.Failed() == true,
// the contract execution reverted (e.g. REVERT opcode was triggered).
func (k Keeper) DerivedEVMCall(
	ctx sdk.Context,
	abi abi.ABI,
	from, contract common.Address,
	value, gasLimit *big.Int,
	commit, gasless, isModuleSender bool,
	manualNonce *uint64,
	method string,
	args ...interface{},
) (*types.MsgEthereumTxResponse, error) {
	data, err := abi.Pack(method, args...)
	if err != nil {
		return nil, errorsmod.Wrap(
			types.ErrABIPack,
			errorsmod.Wrap(err, "failed to create transaction data").Error(),
		)
	}

	resp, err := k.DerivedEVMCallWithData(ctx, from, &contract, data, commit, gasless, isModuleSender, value, gasLimit, manualNonce)
	if err != nil {
		return nil, errorsmod.Wrapf(err, "contract call failed: method '%s', contract '%s'", method, contract)
	}
	return resp, nil
}

// DerivedEVMCallWithData performs an internal EVM contract call using raw call data.
//
// Parameters:
// - from: The sender address.
// - to: The contract address (nil for contract creation).
// - data: Raw EVM call data (ABI-encoded).
// - value: Amount of wei to send with the call.
// - gasLimit: Optional custom gas limit; if nil, gas estimation will be attempted (which may underpredict).
// - commit: Whether to persist state changes (true) or execute as a read-only simulation (false).
//
// Behavior:
//   - If err != nil and msg != nil and msg.Failed() == true, the contract execution reverted.
//     In such cases, msg.Ret contains the revert reason if available (from the REVERT opcode).
//
// Returns:
// - *types.MsgEthereumTxResponse: The result of EVM execution.
// - error: Non-nil if the call failed, including reverts.
func (k Keeper) DerivedEVMCallWithData(
	ctx sdk.Context,
	from common.Address,
	contract *common.Address,
	data []byte,
	commit, gasless, isModuleSender bool,
	value, gasLimit *big.Int,
	manualNonce *uint64,
) (*types.MsgEthereumTxResponse, error) {
	var nonce uint64
	if isModuleSender {
		if manualNonce == nil {
			return nil, errorsmod.Wrap(errortypes.ErrInvalidSequence, "manual nonce required for module sender")
		}
		nonce = *manualNonce
	} else {
		n, err := k.accountKeeper.GetSequence(ctx, from.Bytes())
		if err != nil {
			return nil, err
		}
		nonce = n
	}

	gasCap := config.DefaultGasCap
	if commit && gasLimit == nil {
		args, err := json.Marshal(types.TransactionArgs{
			From: &from,
			To:   contract,
			Data: (*hexutil.Bytes)(&data),
		})
		if err != nil {
			return nil, errorsmod.Wrapf(errortypes.ErrJSONMarshal, "failed to marshal tx args: %s", err.Error())
		}

		gasRes, err := k.EstimateGasInternal(ctx, &types.EthCallRequest{
			Args:   args,
			GasCap: config.DefaultGasCap,
		}, types.Internal)
		if err != nil {
			return nil, err
		}
		gasCap = gasRes.Gas
	}
	if gasLimit != nil {
		gasCap = gasLimit.Uint64()
	}

	msg := core.Message{
		From:            from,
		To:              contract,
		Nonce:           nonce,
		Value:           value,
		GasLimit:        gasCap,
		GasFeeCap:       big.NewInt(0),
		GasTipCap:       big.NewInt(0),
		GasPrice:        big.NewInt(0),
		Data:            data,
		AccessList:      ethtypes.AccessList{},
		SkipNonceChecks: !commit,
		// geth 1.17 folded SkipFromEOACheck into SkipTransactionChecks, which
		// also skips the protocol tx gas-limit check.
		SkipTransactionChecks: !commit,
	}
	tx := ethtypes.NewTx(&ethtypes.DynamicFeeTx{
		Nonce:     msg.Nonce,
		GasFeeCap: msg.GasFeeCap,
		GasTipCap: msg.GasTipCap,
		Gas:       msg.GasLimit,
		To:        msg.To,
		Value:     msg.Value,
		Data:      msg.Data,
	})

	cfg, err := k.EVMConfig(ctx, sdk.ConsAddress(ctx.BlockHeader().ProposerAddress))
	if err != nil {
		return nil, errorsmod.Wrap(err, "failed to load evm config")
	}
	txConfig := k.TxConfig(ctx, tx.Hash())

	// Create a cache context to revert state. The cache context is only committed when both tx and hooks executed successfully.
	// Didn't use `Snapshot` because the context stack has exponential complexity on certain operations,
	// thus restricted to be used only inside `ApplyMessage`.
	tmpCtx, commitState := ctx.CacheContext()

	// v0.6.0: the StateDB is created by the caller and passed in. Derived txs are
	// not precompile calls, so callFromPrecompile is false and the StateDB lives
	// on the cache context (committed only when both tx and hooks succeed).
	stateDB := statedb.New(tmpCtx, &k, txConfig)
	res, err := k.ApplyMessageWithConfig(tmpCtx, stateDB, msg, nil, commit, false, cfg, txConfig, true, nil)
	if err != nil {
		return nil, err
	}

	if commit && !res.Failed() {
		commitState()
	}

	// Emit events and log for the transaction if it is committed
	if commit {
		ethTxHash := res.Hash
		gasUsed := res.GasUsed
		if gasless {
			gasUsed = 0
		}
		attrs := []sdk.Attribute{}
		attrs = append(attrs, []sdk.Attribute{
			sdk.NewAttribute(sdk.AttributeKeyAmount, value.String()),
			// add event for ethereum transaction hash format;
			sdk.NewAttribute(types.AttributeKeyEthereumTxHash, ethTxHash),
			// unique, monotonic eth tx index for this derived tx — drawn from the same
			// block-level counter as standard MsgEthereumTx (advanced below).
			sdk.NewAttribute(types.AttributeKeyTxIndex, strconv.FormatUint(uint64(txConfig.TxIndex), 10)),
			// add event for eth tx gas used, we can't get it from cosmos tx result when it contains multiple eth tx msgs.
			sdk.NewAttribute(types.AttributeKeyTxGasUsed, strconv.FormatUint(gasUsed, 10)),
		}...)

		// recipient: contract address
		if contract != nil {
			attrs = append(attrs, sdk.NewAttribute(types.AttributeKeyRecipient, contract.Hex()))
		}
		if res.Failed() {
			attrs = append(attrs, sdk.NewAttribute(types.AttributeKeyEthereumTxFailed, res.VmError))
		}

		// adding txData for more info in rpc methods in order to parse derived txs
		attrs = append(attrs, sdk.NewAttribute(types.AttributeKeyTxData, hexutil.Encode(msg.Data)))
		// adding nonce for more info in rpc methods in order to parse derived txs
		attrs = append(attrs, sdk.NewAttribute(types.AttributeKeyTxNonce, strconv.FormatUint(nonce, 10)))
		attrs = append(attrs, sdk.NewAttribute(types.AttributeKeyTxGasLimit, strconv.FormatUint(gasCap, 10)))
		// Build the tx_log attributes. On a reverted execution res.Logs is empty,
		// so txLogAttrs ends up empty — but the tx_log event is still emitted below.
		// The JSON-RPC log builder (TxLogsFromEvents) matches logs to txs by
		// position: the Nth tx_log event belongs to the Nth ethereum_tx. So every
		// ethereum_tx must be paired with exactly one tx_log event — an empty one on
		// failure — otherwise logs get misattributed across derived txs in the same
		// block. The failed tx therefore shows a status-0 receipt with no logs.
		txLogAttrs := make([]sdk.Attribute, len(res.Logs))
		for i, log := range res.Logs {
			log.TxHash = ethTxHash
			value, err := json.Marshal(log)
			if err != nil {
				return nil, errorsmod.Wrap(err, "failed to encode log")
			}
			txLogAttrs[i] = sdk.NewAttribute(types.AttributeKeyTxLog, string(value))
		}

		ctx.EventManager().EmitEvents(sdk.Events{
			sdk.NewEvent(
				types.EventTypeEthereumTx,
				attrs...,
			),
			sdk.NewEvent(
				types.EventTypeTxLog,
				txLogAttrs...,
			),
			sdk.NewEvent(
				sdk.EventTypeMessage,
				sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
				sdk.NewAttribute(sdk.AttributeKeySender, from.Hex()),
				sdk.NewAttribute(types.AttributeKeyTxType, strconv.FormatUint(types.DerivedTxType, 10)),
			),
		})

		// Only successful executions contribute to the block bloom.
		// res.Logs is empty on a revert, so a failed tx never touches the bloom.
		//
		// v0.7.0 replaced the block-level bloom transient with a per-(txIndex,
		// msgIndex) object-store entry that EndBlock ORs together
		// (Keeper.CollectTxBloom). AddTxBloom rather than SetTxBloom is used here
		// because one Cosmos message can emit several derived EVM txs, which all
		// share the same object-store key and would otherwise overwrite each other.
		if !res.Failed() {
			logs := types.LogsToEthereum(res.Logs)
			if len(logs) > 0 {
				k.AddTxBloom(ctx, new(big.Int).SetBytes(logsBloom(logs)))
			}
		}

		// NOTE: the log index and the shared monotonic eth-tx index are no longer
		// tracked here. v0.7.0 assigns both block-globally in
		// types.PatchTxResponses, whose rewriteEthTxEventIndex renumbers every
		// ethereum_tx event carrying AttributeKeyTxIndex — which includes the
		// derived-tx event emitted above. That subsumes the SetTxIndexTransient /
		// SetLogSizeTransient accounting this branch used to do by hand.
	}

	if res.Failed() {
		return res, errorsmod.Wrapf(types.ErrVMExecution, "%s: ret 0x%x", res.VmError, res.Ret)
	}

	ctx.GasMeter().ConsumeGas(res.GasUsed, "apply evm message")

	return res, nil
}
