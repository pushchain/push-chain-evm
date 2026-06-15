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

	idxBefore := keeper.GetTxIndexTransient(ctx)
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
	s.Require().Equal(strconv.FormatUint(idxBefore, 10),
		utils.GetEventAttributeValue(ethEvent, evmtypes.AttributeKeyTxIndex))
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
// advances the block-level eth-tx index — the shared counter that also drives
// standard MsgEthereumTx ordering, so the next eth tx gets a unique index.
func (s *KeeperTestSuite) TestDerivedEVMCallWithDataAdvancesTxIndex() {
	s.SetupTest()

	erc20ABI := contracts.ERC20MinterBurnerDecimalsContract.ABI
	wevmos := common.HexToAddress(testconstants.WEVMOSContractMainnet)
	from := s.Keyring.GetAddr(0)
	ctx := s.Network.GetContext()
	keeper := s.Network.App.GetEVMKeeper()

	idxBefore := keeper.GetTxIndexTransient(ctx)
	data, err := erc20ABI.Pack("balanceOf", from)
	s.Require().NoError(err)

	_, err = keeper.DerivedEVMCallWithData(
		ctx, from, &wevmos, data,
		true, false, false, big.NewInt(0), big.NewInt(100_000), nil,
	)
	s.Require().NoError(err)
	s.Require().Equal(idxBefore+1, keeper.GetTxIndexTransient(ctx),
		"a committed derived call must advance the shared eth-tx index by one")
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
