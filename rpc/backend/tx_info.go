package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers/logger"
	"github.com/ethereum/go-ethereum/params"
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	cmtrpcclient "github.com/cometbft/cometbft/rpc/client"
	cmtrpctypes "github.com/cometbft/cometbft/rpc/core/types"

	"github.com/cosmos/evm/mempool/txpool"
	rpctypes "github.com/cosmos/evm/rpc/types"
	servertypes "github.com/cosmos/evm/server/types"
	evmtrace "github.com/cosmos/evm/trace"
	"github.com/cosmos/evm/utils"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// GetTransactionByHash returns the Ethereum format transaction identified by Ethereum transaction hash
func (b *Backend) GetTransactionByHash(ctx context.Context, txHash common.Hash) (result *rpctypes.RPCTransaction, err error) {
	ctx, span := tracer.Start(ctx, "GetTransactionByHash", trace.WithAttributes(attribute.String("txHash", txHash.Hex())))
	defer func() { evmtrace.EndSpanErr(span, err) }()

	res, additional, err := b.GetTxByEthHash(ctx, txHash)
	if err != nil {
		return b.GetTransactionByHashPending(ctx, txHash)
	}

	block, err := b.CometBlockByNumber(ctx, rpctypes.BlockNumber(res.Height))
	if err != nil {
		return nil, err
	}

	// push-chain: the tx decode + MsgEthereumTx assertion upstream does here is
	// performed below instead, guarded by `additional == nil` and with bounds
	// checks, because a derived tx has no decodable carrier message at res.MsgIndex.
	blockRes, err := b.RPCClient.BlockResults(ctx, &block.Block.Height)
	if err != nil {
		b.Logger.Debug("block result not found", "height", block.Block.Height, "error", err.Error())
		return nil, fmt.Errorf("block result not found: %w", err)
	}

	var ethMsg *evmtypes.MsgEthereumTx
	if additional == nil {
		if int(res.TxIndex) >= len(block.Block.Txs) { //nolint:gosec // G115
			return nil, fmt.Errorf("tx index %d out of range for block with %d txs", res.TxIndex, len(block.Block.Txs))
		}
		tx, err := b.ClientCtx.TxConfig.TxDecoder()(block.Block.Txs[res.TxIndex])
		if err != nil {
			b.Logger.Debug("decoding failed", "error", err.Error())
			return nil, fmt.Errorf("failed to decode tx: %w", err)
		}
		msgs := tx.GetMsgs()
		if int(res.MsgIndex) >= len(msgs) { //nolint:gosec // G115
			return nil, fmt.Errorf("msg index %d out of range for tx with %d msgs", res.MsgIndex, len(msgs))
		}
		var ok bool
		ethMsg, ok = msgs[res.MsgIndex].(*evmtypes.MsgEthereumTx)
		if !ok || ethMsg == nil {
			b.Logger.Error("failed to get eth msg from sdk.Msgs")
			return nil, fmt.Errorf("failed to get eth msg from sdk.Msgs")
		}
	} else {
		ethMsg = b.parseDerivedTxFromAdditionalFields(additional)
		if ethMsg == nil {
			b.Logger.Error("failed to get derived eth msg from additional fields")
			return nil, fmt.Errorf("failed to get derived eth msg from additional fields")
		}
	}

	if res.EthTxIndex == -1 {
		// Fallback to find tx index by iterating all valid eth transactions
		idx, ferr := b.FindEthTxIndexByHash(ctx, txHash, block, blockRes)
		if ferr != nil {
			// push-chain: a derived tx is reconstructed from events, so its
			// synthesized hash need not appear among the block's eth messages.
			// Only a native tx must be locatable; shouldn't happen otherwise.
			if additional == nil {
				return nil, ferr
			}
		} else {
			res.EthTxIndex = idx
		}
	}

	baseFee, err := b.BaseFee(ctx, blockRes)
	if err != nil {
		// handle the error for pruned node.
		b.Logger.Error("failed to fetch Base Fee from prunned block. Check node prunning configuration", "height", blockRes.Height, "error", err)
	}

	height := uint64(res.Height)                       //#nosec G115 -- checked for int overflow already
	blockTime := uint64(block.Block.Time.UTC().Unix()) //#nosec G115 -- checked for int overflow already
	index := uint64(res.EthTxIndex)                    //#nosec G115 -- checked for int overflow already
	blockHash := common.BytesToHash(block.BlockID.Hash.Bytes())
	if additional == nil {
		return rpctypes.NewTransactionFromMsg(ethMsg, blockHash, height, blockTime, index, baseFee, b.ChainConfig()), nil
	}
	return rpctypes.NewRPCTransactionFromIncompleteMsg(ethMsg, blockHash, height, index, baseFee, b.EvmChainID, additional.Hash)
}

// GetTransactionByHashPending find pending tx from mempool
func (b *Backend) GetTransactionByHashPending(ctx context.Context, txHash common.Hash) (result *rpctypes.RPCTransaction, err error) {
	ctx, span := tracer.Start(ctx, "GetTransactionByHashPending", trace.WithAttributes(attribute.String("txHash", txHash.Hex())))
	defer func() { evmtrace.EndSpanErr(span, err) }()

	hexTx := txHash.Hex()
	// try to find tx in mempool
	txs, err := b.PendingTransactions(ctx)
	if err != nil {
		b.Logger.Debug("tx not found", "hash", hexTx, "error", err.Error())
		return nil, nil
	}

	for _, tx := range txs {
		msg, err := evmtypes.UnwrapEthereumMsg(tx, txHash)
		if err != nil {
			// not ethereum tx
			continue
		}

		if msg.Hash() == txHash {
			// use zero block values since it's not included in a block yet
			return rpctypes.NewTransactionFromMsg(
				msg,
				common.Hash{},
				uint64(0),
				uint64(0),
				uint64(0),
				nil,
				b.ChainConfig(),
			), nil
		}
	}

	b.Logger.Debug("tx not found", "hash", hexTx)
	return nil, nil
}

// GetGasUsed returns gasUsed from transaction
func (b *Backend) GetGasUsed(res *servertypes.TxResult, _ *big.Int, _ uint64) uint64 {
	return res.GasUsed
}

// GetTransactionReceipt returns the transaction receipt identified by hash.
func (b *Backend) GetTransactionReceipt(ctx context.Context, hash common.Hash) (result map[string]interface{}, err error) {
	ctx, span := tracer.Start(ctx, "GetTransactionReceipt", trace.WithAttributes(attribute.String("hash", hash.Hex())))
	defer func() { evmtrace.EndSpanErr(span, err) }()

	hexTx := hash.Hex()
	b.Logger.Debug("eth_getTransactionReceipt", "hash", hexTx)

	// Retry logic for transaction lookup with exponential backoff
	maxRetries := 10
	baseDelay := 50 * time.Millisecond

	var res *servertypes.TxResult
	var additional *rpctypes.TxResultAdditionalFields

	for attempt := 0; attempt <= maxRetries; attempt++ {
		res, additional, err = b.GetTxByEthHash(ctx, hash)
		if err == nil {
			break // Found the transaction
		}

		if attempt == maxRetries/2 && b.Mempool != nil {
			status := b.Mempool.GetTxPool().Status(hash)
			if status == txpool.TxStatusUnknown {
				break
			}
		}

		if attempt < maxRetries {
			// Exponential backoff: 50ms, 100ms, 200ms
			delay := time.Duration(1<<attempt) * baseDelay
			b.Logger.Debug("tx not found, retrying", "hash", hexTx, "attempt", attempt+1, "delay", delay)
			time.Sleep(delay)
		}
	}

	if err != nil {
		b.Logger.Debug("tx not found after retries", "hash", hexTx, "error", err.Error())
		return nil, nil
	}

	resBlock, err := b.CometBlockByNumber(ctx, rpctypes.BlockNumber(res.Height))
	if err != nil {
		b.Logger.Debug("block not found", "height", res.Height, "error", err.Error())
		return nil, fmt.Errorf("block not found at height %d: %w", res.Height, err)
	}

	var ethMsg *evmtypes.MsgEthereumTx
	if additional == nil {
		// #nosec G115 always in range
		if int(res.TxIndex) >= len(resBlock.Block.Txs) {
			b.Logger.Error("tx out of bounds")
			return nil, fmt.Errorf("tx out of bounds")
		}
		tx, err := b.ClientCtx.TxConfig.TxDecoder()(resBlock.Block.Txs[res.TxIndex])
		if err != nil {
			b.Logger.Debug("decoding failed", "error", err.Error())
			return nil, fmt.Errorf("failed to decode tx: %w", err)
		}
		var ok bool
		ethMsg, ok = tx.GetMsgs()[res.MsgIndex].(*evmtypes.MsgEthereumTx)
		if !ok {
			b.Logger.Error("failed to get eth msg")
			return nil, fmt.Errorf("failed to get eth msg")
		}
	} else {
		ethMsg = b.parseDerivedTxFromAdditionalFields(additional)
		if ethMsg == nil {
			b.Logger.Error("failed to parse derived tx")
			return nil, fmt.Errorf("failed to parse tx")
		}
	}

	blockRes, err := b.RPCClient.BlockResults(ctx, &res.Height)
	if err != nil {
		b.Logger.Debug("failed to retrieve block results", "height", res.Height, "error", err.Error())
		return nil, fmt.Errorf("block result not found at height %d: %w", res.Height, err)
	}

	receipts, err := b.ReceiptsFromCometBlock(ctx, resBlock, blockRes, []*evmtypes.MsgEthereumTx{ethMsg}, []*rpctypes.TxResultAdditionalFields{additional})
	if err != nil {
		return nil, fmt.Errorf("failed to get receipts from comet block")
	}

	var signer ethtypes.Signer
	ethTx := ethMsg.AsTransaction()
	if ethTx.Protected() {
		signer = ethtypes.LatestSignerForChainID(ethTx.ChainId())
	} else {
		signer = ethtypes.FrontierSigner{}
	}
	from, err := ethMsg.GetSenderLegacy(signer)
	if err != nil {
		return nil, fmt.Errorf("failed to get sender: %w", err)
	}

	result, err = rpctypes.RPCMarshalReceipt(receipts[0], ethTx, from)
	if err != nil {
		return nil, err
	}
	// RPCMarshalReceipt computes transactionHash from ethTx.Hash(), which for derived
	// txs is the reconstructed LegacyTx hash — different from the event-emitted hash.
	// Override so eth_getTransactionReceipt agrees with eth_getTransactionByHash.
	if additional != nil {
		result["transactionHash"] = additional.Hash
	}
	return result, nil
}

// GetTransactionLogs returns the transaction logs identified by hash.
func (b *Backend) GetTransactionLogs(ctx context.Context, hash common.Hash) (result []*ethtypes.Log, err error) {
	ctx, span := tracer.Start(ctx, "GetTransactionLogs", trace.WithAttributes(attribute.String("hash", hash.Hex())))
	defer func() { evmtrace.EndSpanErr(span, err) }()

	hexTx := hash.Hex()

	res, additional, err := b.GetTxByEthHash(ctx, hash)
	if err != nil {
		b.Logger.Debug("tx not found", "hash", hexTx, "error", err.Error())
		return nil, nil
	}

	if res.Failed {
		// failed, return empty logs
		return nil, nil
	}

	resBlockResult, err := b.RPCClient.BlockResults(ctx, &res.Height)
	if err != nil {
		b.Logger.Debug("block result not found", "number", res.Height, "error", err.Error())
		return nil, nil
	}
	height, err := utils.SafeUint64(resBlockResult.Height)
	if err != nil {
		return nil, err
	}

	if additional != nil {
		// Derived tx: no MsgEthereumTxResponse in the Cosmos tx Data field.
		// Parse logs from tx_log ABCI events by matching TxHash instead.
		return derivedTxLogsFromEvents(
			resBlockResult.TxsResults[res.TxIndex].Events,
			additional.Hash,
			height,
		)
	}

	index := int(res.MsgIndex) // #nosec G701
	logs, err := evmtypes.DecodeMsgLogs(
		resBlockResult.TxsResults[res.TxIndex].Data,
		index,
		height,
	)
	if err != nil {
		b.Logger.Debug("failed to parse tx logs", "error", err.Error())
	}

	return logs, nil
}

// GetTransactionByBlockHashAndIndex returns the transaction identified by hash and index.
func (b *Backend) GetTransactionByBlockHashAndIndex(ctx context.Context, hash common.Hash, idx hexutil.Uint) (result *rpctypes.RPCTransaction, err error) {
	//nolint:gosec // unlikely
	ctx, span := tracer.Start(ctx, "GetTransactionByBlockHashAndIndex", trace.WithAttributes(attribute.String("hash", hash.Hex()), attribute.Int64("idx", int64(idx))))
	defer func() { evmtrace.EndSpanErr(span, err) }()

	b.Logger.Debug("eth_getTransactionByBlockHashAndIndex", "hash", hash.Hex(), "index", idx)
	sc, ok := b.ClientCtx.Client.(cmtrpcclient.SignClient)
	if !ok {
		return nil, errors.New("invalid rpc client")
	}

	block, err := sc.BlockByHash(ctx, hash.Bytes())
	if err != nil {
		b.Logger.Debug("block not found", "hash", hash.Hex(), "error", err.Error())
		return nil, nil
	}

	if block.Block == nil {
		b.Logger.Debug("block not found", "hash", hash.Hex())
		return nil, nil
	}

	return b.GetTransactionByBlockAndIndex(ctx, block, idx)
}

// GetTransactionByBlockNumberAndIndex returns the transaction identified by number and index.
func (b *Backend) GetTransactionByBlockNumberAndIndex(ctx context.Context, blockNum rpctypes.BlockNumber, idx hexutil.Uint) (result *rpctypes.RPCTransaction, err error) {
	//nolint:gosec // unlikely
	ctx, span := tracer.Start(ctx, "GetTransactionByBlockNumberAndIndex", trace.WithAttributes(attribute.Int64("blockNum", blockNum.Int64()), attribute.Int64("idx", int64(idx))))
	defer func() { evmtrace.EndSpanErr(span, err) }()

	b.Logger.Debug("eth_getTransactionByBlockNumberAndIndex", "number", blockNum, "index", idx)

	block, err := b.CometBlockByNumber(ctx, blockNum)
	if err != nil {
		b.Logger.Debug("block not found", "height", blockNum.Int64(), "error", err.Error())
		return nil, nil
	}

	if block.Block == nil {
		b.Logger.Debug("block not found", "height", blockNum.Int64())
		return nil, nil
	}

	return b.GetTransactionByBlockAndIndex(ctx, block, idx)
}

// derivedTxAdditionalFields rebuilds the TxResultAdditionalFields for a tx located via
// the KV indexer when (and only when) that tx is a derived EVM tx — an internal execution
// recorded only as events, with no embedded MsgEthereumTx to decode. The KV indexer stores
// just the TxResult, so without this the serving paths (GetTransactionByHash / Receipt /
// TraceTransaction) would treat a derived tx as standard and panic on the MsgEthereumTx
// cast. Standard txs return (nil, nil); the IsDerivedTx marker gate keeps their lookups
// cheap (one key read, no event reparse).
func (b *Backend) derivedTxAdditionalFields(ctx context.Context, hash common.Hash, res *servertypes.TxResult) (*rpctypes.TxResultAdditionalFields, error) {
	derived, err := b.Indexer.IsDerivedTx(hash)
	if err != nil {
		return nil, err
	}
	if !derived {
		return nil, nil
	}
	return b.buildDerivedAdditional(ctx, res)
}

// buildDerivedAdditional re-parses the block events for res's Cosmos tx and rebuilds the
// TxResultAdditionalFields for the derived EVM tx at res.MsgIndex. Callers must have
// already confirmed the entry is derived via a marker (IsDerivedTx for the by-hash path,
// IsDerivedTxByBlockAndIndex for the by-block-index path), so a missing or non-derived
// parse result is treated as an error.
func (b *Backend) buildDerivedAdditional(ctx context.Context, res *servertypes.TxResult) (*rpctypes.TxResultAdditionalFields, error) {
	blockRes, err := b.RPCClient.BlockResults(ctx, &res.Height)
	if err != nil {
		return nil, errorsmod.Wrapf(err, "block results for derived tx at height %d", res.Height)
	}
	if int(res.TxIndex) >= len(blockRes.TxsResults) {
		return nil, fmt.Errorf("derived tx index %d out of bounds at height %d", res.TxIndex, res.Height)
	}

	parsedTxs, err := rpctypes.ParseTxResult(blockRes.TxsResults[res.TxIndex], nil)
	if err != nil {
		return nil, errorsmod.Wrapf(err, "parse derived tx events at height %d", res.Height)
	}
	parsed := parsedTxs.GetTxByMsgIndex(int(res.MsgIndex))
	if parsed == nil || parsed.Type != evmtypes.DerivedTxType {
		return nil, fmt.Errorf("derived tx not found in events: height %d, txIndex %d, msgIndex %d",
			res.Height, res.TxIndex, res.MsgIndex)
	}

	return &rpctypes.TxResultAdditionalFields{
		Value:     parsed.Amount,
		Hash:      parsed.Hash,
		TxHash:    parsed.TxHash,
		Type:      parsed.Type,
		Recipient: parsed.Recipient,
		Sender:    parsed.Sender,
		GasUsed:   parsed.GasUsed,
		Data:      parsed.Data,
		Nonce:     parsed.Nonce,
		GasLimit:  &parsed.GasLimit,
	}, nil
}

// GetTxByEthHash uses `/tx_query` to find transaction by ethereum tx hash
// TODO: Don't need to convert once hashing is fixed on CometBFT
// https://github.com/cometbft/cometbft/issues/6539
func (b *Backend) GetTxByEthHash(ctx context.Context, hash common.Hash) (result *servertypes.TxResult, additional *rpctypes.TxResultAdditionalFields, err error) {
	ctx, span := tracer.Start(ctx, "GetTxByEthHash", trace.WithAttributes(attribute.String("hash", hash.Hex())))
	defer func() { evmtrace.EndSpanErr(span, err) }()

	if b.Indexer != nil {
		txRes, err := b.Indexer.GetByTxHash(hash)
		if err == nil {
			// Indexer hit: rebuild additional fields when this is a derived tx.
			additional, derr := b.derivedTxAdditionalFields(ctx, hash, txRes)
			if derr != nil {
				return nil, nil, derr
			}
			return txRes, additional, nil
		}
		// Indexer miss — fall through to CometBFT tx_search for derived tx reconstruction.
	}

	// fallback to CometBFT tx indexer
	query := fmt.Sprintf("%s.%s='%s'", evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyEthereumTxHash, hash.Hex())
	txResult, txAdditional, err := b.QueryCometTxIndexer(ctx, query, func(txs *rpctypes.ParsedTxs) *rpctypes.ParsedTx {
		return txs.GetTxByHash(hash)
	})
	if err != nil {
		return nil, nil, errorsmod.Wrapf(err, "GetTxByEthHash %s", hash.Hex())
	}
	return txResult, txAdditional, nil
}

func (b *Backend) GetTxByEthHashAndMsgIndex(ctx context.Context, hash common.Hash, index int) (*servertypes.TxResult, *rpctypes.TxResultAdditionalFields, error) {
	if b.Indexer != nil {
		txRes, err := b.Indexer.GetByTxHash(hash)
		if err == nil {
			// Indexer hit: rebuild additional fields when this is a derived tx.
			additional, derr := b.derivedTxAdditionalFields(ctx, hash, txRes)
			if derr != nil {
				return nil, nil, derr
			}
			return txRes, additional, nil
		}
		// Indexer miss — fall through to CometBFT tx_search for derived tx reconstruction.
	}

	// fallback to CometBFT tx indexer
	query := fmt.Sprintf("%s.%s='%s'", evmtypes.TypeMsgEthereumTx, evmtypes.AttributeKeyEthereumTxHash, hash.Hex())
	txResult, txAdditional, err := b.QueryCometTxIndexer(ctx, query, func(txs *rpctypes.ParsedTxs) *rpctypes.ParsedTx {
		return txs.GetTxByMsgIndex(index)
	})
	if err != nil {
		return nil, nil, errorsmod.Wrapf(err, "GetTxByEthHash %s", hash.Hex())
	}
	return txResult, txAdditional, nil
}

// GetTxByTxIndex uses `/tx_query` to find transaction by tx index of valid ethereum txs
func (b *Backend) GetTxByTxIndex(ctx context.Context, height int64, index uint) (result *servertypes.TxResult, additional *rpctypes.TxResultAdditionalFields, err error) {
	//nolint:gosec // unlikely
	ctx, span := tracer.Start(ctx, "GetTxByTxIndex", trace.WithAttributes(attribute.Int64("height", height), attribute.Int64("index", int64(index))))
	defer func() { evmtrace.EndSpanErr(span, err) }()

	int32Index := int32(index) //#nosec G115 -- checked for int overflow already
	if b.Indexer != nil {
		txRes, err := b.Indexer.GetByBlockAndIndex(height, int32Index)
		if err == nil {
			// Only derived block-index entries need their additional fields rebuilt (so
			// trace predecessors reconstruct them instead of treating them as standard).
			derived, derr := b.Indexer.IsDerivedTxByBlockAndIndex(height, int32Index)
			if derr != nil {
				return nil, nil, derr
			}
			if derived {
				additional, aerr := b.buildDerivedAdditional(ctx, txRes)
				if aerr != nil {
					return nil, nil, aerr
				}
				return txRes, additional, nil
			}
			return txRes, nil, nil
		}
	}

	// fallback to CometBFT tx indexer
	query := fmt.Sprintf("tx.height=%d AND %s.%s=%d",
		height, evmtypes.TypeMsgEthereumTx,
		evmtypes.AttributeKeyTxIndex, index,
	)
	txResult, txAdditional, err := b.QueryCometTxIndexer(ctx, query, func(txs *rpctypes.ParsedTxs) *rpctypes.ParsedTx {
		return txs.GetTxByTxIndex(int(index)) // #nosec G115 -- checked for int overflow already
	})
	if err != nil {
		return nil, nil, errorsmod.Wrapf(err, "GetTxByTxIndex %d %d", height, index)
	}
	return txResult, txAdditional, nil
}

// QueryCometTxIndexer query tx in CometBFT tx indexer
func (b *Backend) QueryCometTxIndexer(ctx context.Context, query string, txGetter func(*rpctypes.ParsedTxs) *rpctypes.ParsedTx) (result *servertypes.TxResult, additional *rpctypes.TxResultAdditionalFields, err error) {
	ctx, span := tracer.Start(ctx, "QueryCometTxIndexer")
	defer func() { evmtrace.EndSpanErr(span, err) }()

	resTxs, err := b.ClientCtx.Client.TxSearch(ctx, query, false, nil, nil, "")
	if err != nil {
		return nil, nil, err
	}
	if len(resTxs.Txs) == 0 {
		return nil, nil, errors.New("ethereum tx not found")
	}
	txResult := resTxs.Txs[0]
	if !evmtypes.TxSucessOrExpectedFailure(&txResult.TxResult) {
		return nil, nil, errors.New("invalid ethereum tx")
	}

	var tx sdk.Tx
	if txResult.TxResult.Code != 0 {
		// it's only needed when the tx exceeds block gas limit
		tx, err = b.ClientCtx.TxConfig.TxDecoder()(txResult.Tx)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid ethereum tx")
		}
	}

	return rpctypes.ParseTxIndexerResult(txResult, tx, txGetter)
}

// GetTransactionByBlockAndIndex is the common code shared by `GetTransactionByBlockNumberAndIndex` and `GetTransactionByBlockHashAndIndex`.
func (b *Backend) GetTransactionByBlockAndIndex(ctx context.Context, block *cmtrpctypes.ResultBlock, idx hexutil.Uint) (result *rpctypes.RPCTransaction, err error) {
	//nolint:gosec // unlikely
	ctx, span := tracer.Start(ctx, "GetTransactionByBlockAndIndex", trace.WithAttributes(attribute.Int64("blockHeight", block.Block.Height), attribute.Int64("idx", int64(idx))))
	defer func() { evmtrace.EndSpanErr(span, err) }()

	blockRes, err := b.RPCClient.BlockResults(ctx, &block.Block.Height)
	if err != nil {
		return nil, nil
	}

	// push-chain: index positionally into EthMsgsFromCometBlock rather than
	// going through the tx indexer first. Derived txs have no carrier message at
	// res.MsgIndex to decode, and only this list numbers native and derived txs
	// in the same domain, so it is the sole ordering that matches EthTxIndex.
	// #nosec G115 always in range
	i := int(idx)
	ethMsgs, additionals := b.EthMsgsFromCometBlock(ctx, block, blockRes)
	if i >= len(ethMsgs) {
		b.Logger.Debug("block txs index out of bound", "index", i)
		return nil, nil
	}

	msg := ethMsgs[i]
	additional := additionals[i]
	baseFee, err := b.BaseFee(ctx, blockRes)
	if err != nil {
		// handle the error for pruned node.
		b.Logger.Error("failed to fetch Base Fee from prunned block. Check node prunning configuration", "height", block.Block.Height, "error", err)
	}

	height := uint64(block.Block.Height)               // #nosec G115 -- checked for int overflow already
	blockTime := uint64(block.Block.Time.UTC().Unix()) // #nosec G115 -- checked for int overflow already
	index := uint64(idx)                               // #nosec G115 -- checked for int overflow already
	blockHash := common.BytesToHash(block.Block.Hash())
	if additional == nil {
		return rpctypes.NewTransactionFromMsg(msg, blockHash, height, blockTime, index, baseFee, b.ChainConfig()), nil
	}
	return rpctypes.NewRPCTransactionFromIncompleteMsg(msg, blockHash, height, index, baseFee, b.EvmChainID, additional.Hash)
}

// CreateAccessList returns the list of addresses and storage keys used by the transaction (except for the
// sender account and precompiles), plus the estimated gas if the access list were added to the transaction.
func (b *Backend) CreateAccessList(
	ctx context.Context,
	args evmtypes.TransactionArgs,
	blockNrOrHash rpctypes.BlockNumberOrHash,
	overrides *json.RawMessage,
) (result *rpctypes.AccessListResult, err error) {
	ctx, span := tracer.Start(ctx, "CreateAccessList", trace.WithAttributes(attribute.String("from", args.GetFrom().Hex()), attribute.String("blockNrOrHash", unwrapBlockNOrHash(blockNrOrHash))))
	defer func() { evmtrace.EndSpanErr(span, err) }()

	accessList, gasUsed, vmErr, err := b.createAccessList(ctx, args, blockNrOrHash, overrides)
	if err != nil {
		return nil, err
	}

	hexGasUsed := hexutil.Uint64(gasUsed)
	res := rpctypes.AccessListResult{
		AccessList: &accessList,
		GasUsed:    &hexGasUsed,
	}
	if vmErr != nil {
		res.Error = vmErr.Error()
	}
	return &res, nil
}

// createAccessList creates the access list for the transaction.
// It iteratively expands the access list until it converges.
// If the access list has converged, the access list is returned.
// If the access list has not converged, an error is returned.
// If the transaction itself fails, an vmErr is returned.
func (b *Backend) createAccessList(
	ctx context.Context,
	args evmtypes.TransactionArgs,
	blockNrOrHash rpctypes.BlockNumberOrHash,
	overrides *json.RawMessage,
) (_ ethtypes.AccessList, _ uint64, _ error, sysErr error) {
	ctx, span := tracer.Start(ctx, "createAccessList")
	defer func() { evmtrace.EndSpanErr(span, sysErr) }()
	args, err := b.SetTxDefaults(ctx, args)
	if err != nil {
		b.Logger.Error("failed to set tx defaults", "error", err)
		return nil, 0, nil, err
	}

	blockNum, err := b.BlockNumberFromComet(ctx, blockNrOrHash)
	if err != nil {
		b.Logger.Error("failed to get block number", "error", err)
		return nil, 0, nil, err
	}

	addressesToExclude, err := b.getAccessListExcludes(ctx, args, blockNum)
	if err != nil {
		b.Logger.Error("failed to get access list excludes", "error", err)
		return nil, 0, nil, err
	}

	prevTracer, traceArgs, err := b.initAccessListTracer(ctx, args, blockNum, addressesToExclude)
	if err != nil {
		b.Logger.Error("failed to init access list tracer", "error", err)
		return nil, 0, nil, err
	}

	// iteratively expand the access list
	for {
		accessList := prevTracer.AccessList()
		traceArgs.AccessList = &accessList
		res, err := b.DoCall(ctx, *traceArgs, blockNum, overrides)
		if err != nil {
			b.Logger.Error("failed to apply transaction", "error", err)
			return nil, 0, nil, fmt.Errorf("failed to apply transaction: %v err: %v", traceArgs.ToTransaction(ethtypes.LegacyTxType).Hash(), err)
		}

		// Check if access list has converged (no new addresses/slots accessed)
		newTracer := logger.NewAccessListTracer(accessList, addressesToExclude)
		if newTracer.Equal(prevTracer) {
			b.Logger.Info("access list converged", "accessList", accessList)
			var vmErr error
			if res.VmError != "" {
				b.Logger.Error("vm error after access list converged", "vmError", res.VmError)
				vmErr = errors.New(res.VmError)
			}
			return accessList, res.GasUsed, vmErr, nil
		}
		prevTracer = newTracer
	}
}

// getAccessListExcludes returns the addresses to exclude from the access list.
// This includes the sender account, the target account (if provided), precompiles,
// and any addresses in the authorization list.
func (b *Backend) getAccessListExcludes(ctx context.Context, args evmtypes.TransactionArgs, blockNum rpctypes.BlockNumber) (_ map[common.Address]struct{}, err error) {
	ctx, span := tracer.Start(ctx, "getAccessListExcludes")
	defer func() { evmtrace.EndSpanErr(span, err) }()
	header, err := b.HeaderByNumber(ctx, blockNum)
	if err != nil {
		b.Logger.Error("failed to get header by number", "error", err)
		return nil, err
	}

	// exclude sender and precompiles
	addressesToExclude := make(map[common.Address]struct{})
	addressesToExclude[args.GetFrom()] = struct{}{}
	if args.To != nil {
		addressesToExclude[*args.To] = struct{}{}
	}

	isMerge := b.ChainConfig().MergeNetsplitBlock != nil
	precompiles := vm.ActivePrecompiles(b.ChainConfig().Rules(header.Number, isMerge, header.Time))
	for _, addr := range precompiles {
		addressesToExclude[addr] = struct{}{}
	}

	// check if enough gas was provided to cover all authorization lists
	maxAuthorizations := uint64(*args.Gas) / params.CallNewAccountGas
	if uint64(len(args.AuthorizationList)) > maxAuthorizations {
		b.Logger.Error("insufficient gas to process all authorizations", "maxAuthorizations", maxAuthorizations)
		return nil, errors.New("insufficient gas to process all authorizations")
	}

	for _, auth := range args.AuthorizationList {
		// validate authorization (duplicating stateTransition.validateAuthorization() logic from geth: https://github.com/ethereum/go-ethereum/blob/bf8f63dcd27e178bd373bfe41ea718efee2851dd/core/state_transition.go#L575)
		nonceOverflow := auth.Nonce+1 < auth.Nonce
		invalidChainID := !auth.ChainID.IsZero() && auth.ChainID.CmpBig(b.ChainConfig().ChainID) != 0
		if nonceOverflow || invalidChainID {
			b.Logger.Error("invalid authorization", "auth", auth)
			continue
		}
		if authority, err := auth.Authority(); err == nil {
			addressesToExclude[authority] = struct{}{}
		}
	}

	b.Logger.Debug("access list excludes created", "addressesToExclude", addressesToExclude)
	return addressesToExclude, nil
}

// initAccessListTracer initializes the access list tracer for the transaction.
// It sets the default call arguments and creates a new access list tracer.
// If an access list is provided in args, it uses that instead of creating a new one.
func (b *Backend) initAccessListTracer(ctx context.Context, args evmtypes.TransactionArgs, blockNum rpctypes.BlockNumber, addressesToExclude map[common.Address]struct{}) (*logger.AccessListTracer, *evmtypes.TransactionArgs, error) {
	ctx, span := tracer.Start(ctx, "initAccessListTracer")
	defer span.End()
	header, err := b.HeaderByNumber(ctx, blockNum)
	if err != nil {
		b.Logger.Error("failed to get header by number", "error", err)
		return nil, nil, err
	}

	if args.Nonce == nil {
		pending := blockNum == rpctypes.EthPendingBlockNumber
		nonce, err := b.getAccountNonce(ctx, args.GetFrom(), pending, blockNum.Int64(), b.Logger)
		if err != nil {
			b.Logger.Error("failed to get account nonce", "error", err)
			return nil, nil, err
		}
		nonce64 := hexutil.Uint64(nonce)
		args.Nonce = &nonce64
	}
	if err = args.CallDefaults(b.RPCGasCap(), header.BaseFee, b.ChainConfig().ChainID); err != nil {
		b.Logger.Error("failed to set default call args", "error", err)
		return nil, nil, err
	}

	tracer := logger.NewAccessListTracer(nil, addressesToExclude)
	if args.AccessList != nil {
		tracer = logger.NewAccessListTracer(*args.AccessList, addressesToExclude)
	}

	b.Logger.Debug("access list tracer initialized", "tracer", tracer)
	return tracer, &args, nil
}
