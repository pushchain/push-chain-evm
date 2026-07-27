package vm

import (
	"math/big"
	"strconv"

	"github.com/ethereum/go-ethereum/common"

	abcitypes "github.com/cometbft/cometbft/abci/types"

	"github.com/cosmos/evm/contracts"
	testconstants "github.com/cosmos/evm/testutil/constants"
	"github.com/cosmos/evm/testutil/integration/evm/utils"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// findEvent returns the first ABCI event of the given type, or false if none.
func findEvent(events []abcitypes.Event, eventType string) (abcitypes.Event, bool) {
	for _, e := range events {
		if e.Type == eventType {
			return e, true
		}
	}
	return abcitypes.Event{}, false
}

// countEvents returns how many ABCI events of the given type are present.
func countEvents(events []abcitypes.Event, eventType string) int {
	n := 0
	for _, e := range events {
		if e.Type == eventType {
			n++
		}
	}
	return n
}

// TestDerivedEVMCallModuleSenderRequiresNonce checks the module-sender guard: a
// derived call from a module account must carry a manual nonce (module accounts
// have no auth sequence to draw from), and otherwise errors out before the EVM.
func (s *KeeperTestSuite) TestDerivedEVMCallModuleSenderRequiresNonce() {
	s.SetupTest()

	erc20ABI := contracts.ERC20MinterBurnerDecimalsContract.ABI
	wevmos := common.HexToAddress(testconstants.WEVMOSContractMainnet)
	from := s.Keyring.GetAddr(0)

	_, err := s.Network.App.GetEVMKeeper().DerivedEVMCall(
		s.Network.GetContext(), erc20ABI, from, wevmos,
		big.NewInt(0), big.NewInt(100_000),
		true,  // commit
		false, // gasless
		true,  // isModuleSender
		nil,   // manualNonce — missing on purpose
		"balanceOf", from,
	)
	s.Require().Error(err)
	s.Require().ErrorContains(err, "manual nonce required for module sender")
}

// TestDerivedEVMCallWithDataEmitsDerivedTxEvents verifies that a committed
// derived call emits the events the JSON-RPC layer relies on to reconstruct a
// derived EVM tx: an ethereum_tx event carrying hash/index/data/recipient, and a
// message event tagged with the derived tx type (99) so the indexer can tell it
// apart from a standard MsgEthereumTx.
func (s *KeeperTestSuite) TestDerivedEVMCallWithDataEmitsDerivedTxEvents() {
	s.SetupTest()

	erc20ABI := contracts.ERC20MinterBurnerDecimalsContract.ABI
	wevmos := common.HexToAddress(testconstants.WEVMOSContractMainnet)
	from := s.Keyring.GetAddr(0)
	ctx := s.Network.GetContext()
	keeper := s.Network.App.GetEVMKeeper()

	data, err := erc20ABI.Pack("balanceOf", from)
	s.Require().NoError(err)

	res, err := keeper.DerivedEVMCallWithData(
		ctx, from, &wevmos, data,
		true,  // commit
		false, // gasless
		false, // isModuleSender — draw the nonce from the account sequence
		big.NewInt(0), big.NewInt(100_000), nil,
	)
	s.Require().NoError(err)
	s.Require().False(res.Failed())

	abciEvents := ctx.EventManager().Events().ToABCIEvents()

	ethEvent, ok := findEvent(abciEvents, evmtypes.EventTypeEthereumTx)
	s.Require().True(ok, "derived call must emit an ethereum_tx event")
	s.Require().NotEmpty(utils.GetEventAttributeValue(ethEvent, evmtypes.AttributeKeyEthereumTxHash))
	// The tx index attribute must be present and numeric: cosmos/evm v0.7.0
	// renumbers it block-globally in types.PatchTxResponses, and an ethereum_tx
	// event without this attribute would be skipped by that pass and so fall out
	// of the shared eth-tx numbering.
	rawIdx := utils.GetEventAttributeValue(ethEvent, evmtypes.AttributeKeyTxIndex)
	s.Require().NotEmpty(rawIdx, "derived ethereum_tx event must carry a tx index attribute")
	_, parseErr := strconv.ParseUint(rawIdx, 10, 64)
	s.Require().NoError(parseErr, "tx index attribute must be a decimal integer")
	s.Require().NotEmpty(utils.GetEventAttributeValue(ethEvent, evmtypes.AttributeKeyTxData))
	s.Require().Equal(wevmos.Hex(),
		utils.GetEventAttributeValue(ethEvent, evmtypes.AttributeKeyRecipient))

	var txType string
	for _, e := range abciEvents {
		if e.Type != sdk.EventTypeMessage {
			continue
		}
		if v := utils.GetEventAttributeValue(e, evmtypes.AttributeKeyTxType); v != "" {
			txType = v
			break
		}
	}
	s.Require().Equal(strconv.FormatUint(evmtypes.DerivedTxType, 10), txType,
		"message event must tag the execution as a derived tx (type 99)")
}

// TestDerivedEVMCallWithDataAdvancesTxIndex verifies a committed derived call
// contributes exactly one ethereum_tx event to the block-global eth-tx
// numbering — the shared ordering that also covers standard MsgEthereumTx, so
// the next eth tx gets a unique index.
//
// cosmos/evm v0.7.0 removed the eth-tx-index transient this test used to read;
// the index is now assigned block-globally by types.PatchTxResponses, whose
// rewriteEthTxEventIndex advances one step per ethereum_tx event that carries
// AttributeKeyTxIndex. Emitting exactly one such event is therefore the
// v0.7.0 expression of the same invariant.
func (s *KeeperTestSuite) TestDerivedEVMCallWithDataAdvancesTxIndex() {
	s.SetupTest()

	erc20ABI := contracts.ERC20MinterBurnerDecimalsContract.ABI
	wevmos := common.HexToAddress(testconstants.WEVMOSContractMainnet)
	from := s.Keyring.GetAddr(0)
	ctx := s.Network.GetContext()
	keeper := s.Network.App.GetEVMKeeper()

	data, err := erc20ABI.Pack("balanceOf", from)
	s.Require().NoError(err)

	before := countEvents(ctx.EventManager().Events().ToABCIEvents(), evmtypes.EventTypeEthereumTx)

	_, err = keeper.DerivedEVMCallWithData(
		ctx, from, &wevmos, data,
		true, false, false, big.NewInt(0), big.NewInt(100_000), nil,
	)
	s.Require().NoError(err)

	after := countEvents(ctx.EventManager().Events().ToABCIEvents(), evmtypes.EventTypeEthereumTx)
	s.Require().Equal(before+1, after,
		"a committed derived call must contribute exactly one ethereum_tx event to the shared eth-tx index")
}

// TestDerivedEVMCallWithDataGasless verifies the gasless flag zeroes the gas
// reported in the emitted event, even though the EVM still meters gas internally
// (used by sponsored / module-initiated derived calls).
func (s *KeeperTestSuite) TestDerivedEVMCallWithDataGasless() {
	s.SetupTest()

	erc20ABI := contracts.ERC20MinterBurnerDecimalsContract.ABI
	wevmos := common.HexToAddress(testconstants.WEVMOSContractMainnet)
	from := s.Keyring.GetAddr(0)
	ctx := s.Network.GetContext()
	keeper := s.Network.App.GetEVMKeeper()

	data, err := erc20ABI.Pack("balanceOf", from)
	s.Require().NoError(err)

	res, err := keeper.DerivedEVMCallWithData(
		ctx, from, &wevmos, data,
		true, // commit
		true, // gasless
		false,
		big.NewInt(0), big.NewInt(100_000), nil,
	)
	s.Require().NoError(err)
	s.Require().False(res.Failed())

	ethEvent, ok := findEvent(ctx.EventManager().Events().ToABCIEvents(), evmtypes.EventTypeEthereumTx)
	s.Require().True(ok)
	s.Require().Equal("0", utils.GetEventAttributeValue(ethEvent, evmtypes.AttributeKeyTxGasUsed),
		"gasless derived call must report zero gas in its event")
	s.Require().Greater(res.GasUsed, uint64(0), "the EVM still meters gas internally")
}

// TestDerivedEVMCallWithDataRevertDoesNotMutateBloom is a regression test for F-2026-17738
// (a reverted derived execution must not mutate the block bloom) and the failure half of
// F-2026-17736 (failed execution must not commit). An ERC20 transfer of more than the
// sender holds reverts; the call must surface the error and leave the block bloom
// untouched.
func (s *KeeperTestSuite) TestDerivedEVMCallWithDataRevertDoesNotMutateBloom() {
	s.SetupTest()

	erc20ABI := contracts.ERC20MinterBurnerDecimalsContract.ABI
	wevmos := common.HexToAddress(testconstants.WEVMOSContractMainnet)
	from := s.Keyring.GetAddr(0)
	ctx := s.Network.GetContext()
	keeper := s.Network.App.GetEVMKeeper()

	bloomBefore := keeper.GetTxBloom(ctx).Bytes()

	// Transfer far more than the sender could hold → ERC20 reverts on insufficient
	// balance. NOTE: the amount must exceed the sender's balance; under the v0.7.0
	// test genesis the keyring accounts are pre-funded in this token, so the
	// previous "transfer 1 from a zero-balance account" no longer reverts.
	hugeAmt := new(big.Int).Lsh(big.NewInt(1), 200)
	data, err := erc20ABI.Pack("transfer", common.HexToAddress("0x000000000000000000000000000000000000dEaD"), hugeAmt)
	s.Require().NoError(err)

	res, err := keeper.DerivedEVMCallWithData(
		ctx, from, &wevmos, data,
		true, false, false, big.NewInt(0), big.NewInt(100_000), nil,
	)
	s.Require().Error(err, "a reverting derived call must surface an error")
	s.Require().NotNil(res)
	s.Require().True(res.Failed(), "the EVM execution reverted")

	s.Require().Equal(bloomBefore, keeper.GetTxBloom(ctx).Bytes(),
		"a reverted derived execution must not mutate the block bloom (F-2026-17738)")
}
