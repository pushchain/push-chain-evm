# CHANGELOG

## v0.5.1

### DEPENDENCIES

### IMPROVEMENTS

### FEATURES

### BUG FIXES

- Honour the `gasCap` argument in `CallEVMWithData`. The parameter was accepted and then ignored -
  `GasLimit` was hardcoded to `config.DefaultGasCap` - so callers passing a cap to sandbox EVM work
  (the IBC callback keeper passes its remaining Cosmos gas) got a message that could burn up to 25M
  gas regardless. `DefaultGasCap` remains the ceiling, so a caller can only narrow the limit. This
  matches `DerivedEVMCallWithData` and the shape upstream cosmos/evm settled on. Also routes the
  timeout callback's EVM call through `cachedCtx`, as the acknowledgement path already does.
- Resolve the ABI method at the top of every stateful precompile's `Run`, before `RunNativeAction`
  is entered. The native-action preamble takes a cache context, snapshots the multi-store and
  commits the state DB cache - replaying the caller's entire dirty account and storage set - and
  only then does `SetupABI` get around to resolving the selector and failing. `RequiredGas` returns
  0 for an unresolvable selector, so none of that work was charged: a contract could dirty a large
  storage set and then hand `bank`, `gov`, `staking`, `distribution`, `ics20`, `slashing`, `erc20`
  or `werc20` four random bytes to have the whole set replayed for free, up to 20 times per
  transaction. The check goes through the new `cmn.ResolveMethod`, which `SetupABI` now also uses,
  so the fallback/receive special cases WERC20 depends on keep working and the revert is unchanged.
- Make `ConvertCoinNativeERC20` atomic. It escrowed the sender's coins onto the erc20 module
  account before calling the EVM, and `CallEVMWithData` caches only the EVM execution, so a VM
  failure discarded the EVM cache while leaving the bank escrow committed. On the IBC error-ACK /
  timeout refund path, which deliberately swallows a conversion error so a failed re-wrap cannot
  undo the refund, that stranded the sender's coins on the module account permanently - contradicting
  the `OnAcknowledgementPacket` / `OnTimeoutPacket` comments promising the user keeps the bank token.
  The escrow, the EVM transfer and the burn now all run on a branched context and commit together.
- Run the gov precompile's `getTallyResult` against a throwaway cache. The SDK's `TallyResult` query
  delegates to `Keeper.Tally`, which deletes every vote it counts; that is safe on the SDK's own
  query paths because a gRPC query runs against a context that is never committed, but precompiles
  commit their state DB cache context unconditionally. A plain EVM call to `getTallyResult` on a
  proposal in the voting period therefore deleted that proposal's votes permanently. The query's
  store writes are now discarded, leaving the tally itself unchanged.
- Verify the Ethereum sender in `Keeper.EthereumTx` before applying the transaction. The EVM ante
  handler only runs over a tx's top-level messages, so an `MsgEthereumTx` nested inside a
  dispatching module (`x/authz`, `x/group`, `x/gov`, a CosmWasm stargate/`Any` message, ICA) reached
  the executor with its signature never checked, letting a victim-signed transaction be replayed as
  the victim. `VerifySender` is now required before `ApplyTransaction`, so `From` is always the
  ECDSA-recovered signer regardless of how the message arrived.
- Clamp the fee market's cumulative gas wanted instead of failing the block. `EndBlock` converted
  both the block's cumulative `gasWanted` and its `gasUsed` to `int64` and returned an error when
  either did not fit. `gasWanted` is an unchecked uint64 sum of per-transaction declarations, and
  under `max_gas: -1` nothing bounds a single declaration, so two transactions declaring `MaxInt64`
  sum into `(MaxInt64, MaxUint64]` with neither being individually rejectable. An error out of
  `EndBlock` surfaces through `FinalizeBlock` after the block has been decided, leaving CometBFT at
  height H and the application at H-1. Both values are now clamped to `MaxInt64` and logged.
  `AddTransientGasWanted` also saturates instead of wrapping, so the total can never silently roll
  over to a small number.
- Reject a `x/erc20` conversion whose token `transfer` emits an unexpected `Approval` event. Both
  native-ERC20 convert directions escrow the token and mint (or burn) the paired coins, checking
  only that the transfer succeeded and that the balance moved by the right amount. A registered
  token that also grants a third party an allowance over the recipient satisfies both checks: the
  escrow only drains later, when that allowance is spent, leaving the minted coins unbacked. The
  `validateApprovalEventDoesNotExist` guard, already documented by both convert doc-comments but
  never called, now runs on both paths.
- Charge the parent Cosmos gas meter for failed EVM calls in `CallEVMWithData` and
  `DerivedEVMCallWithData`. Both functions returned on `res.Failed()` before reaching
  `ctx.GasMeter().ConsumeGas(res.GasUsed)`, so a reverting — or deliberately out-of-gas —
  internal call performed real EVM work that cost the enclosing Cosmos transaction nothing
  beyond the incidental KV-store gas. `DerivedEVMCallWithData` additionally clamps a
  caller-supplied `gasLimit` to `config.DefaultGasCap`; on an out-of-gas halt `res.GasUsed`
  equals the gas cap, so without the clamp the caller would pick how much gas the enclosing
  transaction is forced to consume and could panic it with `OutOfGas`. `CallEVMWithData`
  needs no clamp — its message gas limit is already the hardcoded `config.DefaultGasCap`.
- [\#690](https://github.com/cosmos/evm/pull/690) Fix Ledger hardware wallet support for coin type 60.
- [\#769](https://github.com/cosmos/evm/pull/769) Fix erc20 ibc middleware to not to validate sender address format.
- [\#790](https://github.com/cosmos/evm/pull/790) fix panic in historical query due to missing EvmCoinInfo.
- [\#816](https://github.com/cosmos/evm/pull/816) Avoid nil pointer when RPC requests execute before evmCoinInfo initialization in PreBlock with defaultEvmCoinInfo fallback.

## v0.5.0

### DEPENDENCIES

### BUG FIXES

- [\#471](https://github.com/cosmos/evm/pull/471) Notify new block for mempool in time
- [\#492](https://github.com/cosmos/evm/pull/492) Duplicate case switch to avoid empty execution block
- [\#509](https://github.com/cosmos/evm/pull/509) Allow value with slashes when query token_pairs
- [\#495](https://github.com/cosmos/evm/pull/495) Allow immediate SIGINT interrupt when mempool is not empty
- [\#416](https://github.com/cosmos/evm/pull/416) Fix regression in CometBlockResultByNumber when height is 0 to use the latest block. This fixes eth_getFilterLogs RPC.
- [\#545](https://github.com/cosmos/evm/pull/545) Check if mempool is not nil before accepting nonce gap error tx.
- [\#585](https://github.com/cosmos/evm/pull/585) Use zero constructor to avoid nil pointer panic when BaseFee is 0d
- [\#591](https://github.com/cosmos/evm/pull/591) CheckTxHandler should handle "invalid nonce" tx
- [\#642](https://github.com/cosmos/evm/pull/642) "tx not found in mempool" error on chain startup
- [\#643](https://github.com/cosmos/evm/pull/643) Support for mnemonic source (file, stdin,etc) flag in key add command.
- [\#645](https://github.com/cosmos/evm/pull/645) Align precise bank keeper for correct decimal conversion in evmd.
- [\#656](https://github.com/cosmos/evm/pull/656) Fix race condition in concurrent usage of mempool StateAt and NotifyNewBlock methods.
- [\#658](https://github.com/cosmos/evm/pull/658) Fix race condition between legacypool's RemoveTx and runReorg.
- [\#687](https://github.com/cosmos/evm/pull/687) Avoid blocking node shutdown when evm indexer is enabled, log startup failures instead of using errgroup.
- [\#689](https://github.com/cosmos/evm/pull/689) Align debug addr for hex address.
- [\#668](https://github.com/cosmos/evm/pull/668) Fix panic in legacy mempool when Reset() was called with a skipped header between old and new block.
- [\#723](https://github.com/cosmos/evm/pull/723) Fix TransactionIndex in receipt generation to use actual EthTxIndex instead of loop index.
- [\#729](https://github.com/cosmos/evm/pull/729) Remove non-deterministic state mutation from EVM pre-blocker.
- [\#725](https://github.com/cosmos/evm/pull/725) Fix inconsistent block hash in json-rpc.
- [\#727](https://github.com/cosmos/evm/pull/727) Avoid nil pointer for `tx evm raw` due to uninitialized EVM coin info.
- [\#730](https://github.com/cosmos/evm/pull/730) Fix panic if evm mempool not used.
- [\#733](https://github.com/cosmos/evm/pull/733) Avoid rejecting tx with unsupported extension option for ExtensionOptionDynamicFeeTx.
- [\#736](https://github.com/cosmos/evm/pull/736) Add InitEvmCoinInfo upgrade to avoid panic when denom is not registered.

### IMPROVEMENTS

- [\#708](https://github.com/cosmos/evm/pull/708) Add configurable testnet validator powers
- [\#698](https://github.com/cosmos/evm/pull/698) Expose mempool configuration flags and move mempool configuration in app.go to helper
- [\#538](https://github.com/cosmos/evm/pull/538) Optimize `eth_estimateGas` gRPC path: short-circuit plain transfers, add optimistic gas bound based on `MaxUsedGas`.
- [\#513](https://github.com/cosmos/evm/pull/513) Replace `TestEncodingConfig` with production `EncodingConfig` in encoding package to remove test dependencies from production code.
- [\#467](https://github.com/cosmos/evm/pull/467) Replace GlobalEVMMempool by passing to JSONRPC on initiate.
- [\#352](https://github.com/cosmos/evm/pull/352) Remove the creation of a Geth EVM instance, stateDB during the AnteHandler balance check.
- [\#496](https://github.com/cosmos/evm/pull/496) Simplify mempool instantiation by using configs instead of objects.
- [\#512](https://github.com/cosmos/evm/pull/512) Add integration test for appside mempool.
- [\#568](https://github.com/cosmos/evm/pull/568) Avoid unnecessary block notifications when the event bus is already set up.
- [\#511](https://github.com/cosmos/evm/pull/511) Minor code cleanup for `AddPrecompileFn`.
- [\#576](https://github.com/cosmos/evm/pull/576) Parse logs from the txResult.Data and avoid emitting EVM events to cosmos-sdk events.
- [\#584](https://github.com/cosmos/evm/pull/584) Fill block hash and timestamp for json rpc.
- [\#582](https://github.com/cosmos/evm/pull/582) Add block max-gas (from genesis.json) and new min-tip (from app.toml/flags) ingestion into mempool config
- [\#580](https://github.com/cosmos/evm/pull/580) add appside mempool e2e test
- [\#598](https://github.com/cosmos/evm/pull/598) Reduce number of times CreateQueryContext in mempool.
- [\#606](https://github.com/cosmos/evm/pull/606) Regenerate mock file for bank keeper related test.
- [\#609](https://github.com/cosmos/evm/pull/609) Make `erc20Keeper` optional in the EVM keeper
- [\#624](https://github.com/cosmos/evm/pull/624) Cleanup unnecessary `fix-revert-gas-refund-height`.
- [\#635](https://github.com/cosmos/evm/pull/635) Move DefaultStaticPrecompiles to /evm and allow projects to set it by default alongside the keeper.
- [\#639](https://github.com/cosmos/evm/pull/639) Remove `/types` and move types into respective folders.
- [\#630](https://github.com/cosmos/evm/pull/630) Reduce feemarket parameter loading to minimize memory allocations.
- [\#577](https://github.com/cosmos/evm/pull/577) Cleanup precompiles boilerplate code.
- [\#648](https://github.com/cosmos/evm/pull/648) Move all `ante` logic such as `NewAnteHandler` from the `evmd` package to `evm/ante` so it can be used as library functions.
- [\#659](https://github.com/cosmos/evm/pull/659) Move configs out of EVMD and deduplicate configs
- [\#664](https://github.com/cosmos/evm/pull/664) Add EIP-7702 integration test
- [\#684](https://github.com/cosmos/evm/pull/684) Add unit test cases for EIP-7702
- [\#685](https://github.com/cosmos/evm/pull/685) Add EIP-7702 e2e test
- [\#680](https://github.com/cosmos/evm/pull/680) Introduce a `StaticPrecompiles` builder
- [\#701](https://github.com/cosmos/evm/pull/701) Add address codec support to ERC20 IBC callbacks to handle hex addresses in addition to bech32 addresses.
- [\#704](https://github.com/cosmos/evm/pull/704) Fix EIP-7702 test cases
- [\#709](https://github.com/cosmos/evm/pull/709) Fix mempool e2e test
- [\#710](https://github.com/cosmos/evm/pull/710) Fix EoA-CA Identification logic
- [\#711](https://github.com/cosmos/evm/pull/711) Add debug_traceCall api
- [\#734](https://github.com/cosmos/evm/pull/734) Disable evm mempool if max-txs set to -1.


### FEATURES

- [\#665](https://github.com/cosmos/evm/pull/665) Add EvmCodec address codec implementation
- [\#346](https://github.com/cosmos/evm/pull/346) Add eth_createAccessList method and implementation
- [\#337](https://github.com/cosmos/evm/pull/337) Support state overrides in eth_call.
- [\#502](https://github.com/cosmos/evm/pull/502) Add block time in derived logs.
- [\#633](https://github.com/cosmos/evm/pull/633) go-ethereum metrics are now emitted on a separate server. default address: 127.0.0.1:8100.
- [\#650](https://github.com/cosmos/evm/pull/650) Make staking precompile queries return the full validators' description structure.

### STATE BREAKING

### API-BREAKING

- [\#477](https://github.com/cosmos/evm/pull/477) Refactor precompile constructors to accept keeper interfaces instead of concrete implementations, breaking the existing `NewPrecompile` function signatures.
- [\#594](https://github.com/cosmos/evm/pull/594) Remove all usage of x/params
- [\#577](https://github.com/cosmos/evm/pull/577) Changed the way to create a stateful precompile based on the cmn.Precompile, change `NewPrecompile` to not return error.
- [\#661](https://github.com/cosmos/evm/pull/661) Removes evmAppOptions from the repository and moves initialization to genesis. Chains must now have a display and denom metadata set for the defined EVM denom in the bank module's metadata.


## v0.4.1

### DEPENDENCIES

- [\#459](https://github.com/cosmos/evm/pull/459) Update `cosmossdk.io/log` to `v1.6.1` to support Go `v1.25.0+`.
- [\#435](https://github.com/cosmos/evm/pull/435) Update Cosmos SDK to `v0.53.4` and CometBFT to `v0.38.18`.

### BUG FIXES

- [\#179](https://github.com/cosmos/evm/pull/179) Fix compilation error in server/start.go
- [\#245](https://github.com/cosmos/evm/pull/245) Use PriorityMempool with signer extractor to prevent missing signers error in tx execution
- [\#289](https://github.com/cosmos/evm/pull/289) Align revert reason format with go-ethereum (return hex-encoded result)
- [\#291](https://github.com/cosmos/evm/pull/291) Use proper address codecs in precompiles for bech32/hex conversion
- [\#296](https://github.com/cosmos/evm/pull/296) Add sanity checks to trace_tx RPC endpoint
- [\#316](https://github.com/cosmos/evm/pull/316) Fix estimate gas to handle missing fields for new transaction types
- [\#330](https://github.com/cosmos/evm/pull/330) Fix error propagation in BlockHash RPCs and address test flakiness
- [\#332](https://github.com/cosmos/evm/pull/332) Fix non-determinism in state transitions
- [\#350](https://github.com/cosmos/evm/pull/350) Fix p256 precompile test flakiness
- [\#376](https://github.com/cosmos/evm/pull/376) Fix precompile initialization for local node development script
- [\#384](https://github.com/cosmos/evm/pull/384) Fix debug_traceTransaction RPC failing with block height mismatch errors
- [\#441](https://github.com/cosmos/evm/pull/441) Align precompiles map with available static check to Prague.
- [\#452](https://github.com/cosmos/evm/pull/452) Cleanup unused cancel function in filter.
- [\#454](https://github.com/cosmos/evm/pull/454) Align multi decode functions instead of string contains check in HexAddressFromBech32String.
- [\#468](https://github.com/cosmos/evm/pull/468) Add pagination flags to `token-pairs` to improve query flexibility.

### IMPROVEMENTS

- [\#294](https://github.com/cosmos/evm/pull/294) Enforce single EVM transaction per Cosmos transaction for security
- [\#299](https://github.com/cosmos/evm/pull/299) Update dependencies for security and performance improvements
- [\#307](https://github.com/cosmos/evm/pull/307) Preallocate EVM access_list for better performance
- [\#317](https://github.com/cosmos/evm/pull/317) Fix EmitApprovalEvent to use owner address instead of precompile address
- [\#345](https://github.com/cosmos/evm/pull/345) Fix gas cap calculation and fee rounding errors in ante handler benchmarks
- [\#347](https://github.com/cosmos/evm/pull/347) Add loop break labels for optimization
- [\#370](https://github.com/cosmos/evm/pull/370) Use larger CI runners for resource-intensive tests
- [\#373](https://github.com/cosmos/evm/pull/373) Apply security audit patches
- [\#377](https://github.com/cosmos/evm/pull/377) Apply audit-related commit 388b5c0
- [\#382](https://github.com/cosmos/evm/pull/382) Post-audit security fixes (batch 1)
- [\#388](https://github.com/cosmos/evm/pull/388) Post-audit security fixes (batch 2)
- [\#389](https://github.com/cosmos/evm/pull/389) Post-audit security fixes (batch 3)
- [\#392](https://github.com/cosmos/evm/pull/392) Post-audit security fixes (batch 5)
- [\#398](https://github.com/cosmos/evm/pull/398) Post-audit security fixes (batch 4)
- [\#442](https://github.com/cosmos/evm/pull/442) Prevent nil pointer by checking error in gov precompile FromResponse.
- [\#387](https://github.com/cosmos/evm/pull/387) (Experimental) EVM-compatible appside mempool
- [\#476](https://github.com/cosmos/evm/pull/476) Add revert error e2e tests for contract and precompile calls
- [\#599](https://github.com/cosmos/evm/pull/599) Align jsonrpc apis with geth v1.16.3

### FEATURES

- [\#253](https://github.com/cosmos/evm/pull/253) Add comprehensive Solidity-based end-to-end tests for precompiles
- [\#301](https://github.com/cosmos/evm/pull/301) Add 4-node localnet infrastructure for testing multi-validator setups
- [\#304](https://github.com/cosmos/evm/pull/304) Add system test framework for integration testing
- [\#344](https://github.com/cosmos/evm/pull/344) Add txpool RPC namespace stubs in preparation for app-side mempool implementation
- [\#440](https://github.com/cosmos/evm/pull/440) Enforce app creator returning application implement AppWithPendingTxStream in build time.

### STATE BREAKING

### API-BREAKING

- [\#456](https://github.com/cosmos/evm/pull/456) Remove non–go-ethereum JSON-RPC methods to align with Geth’s surface
- [\#443](https://github.com/cosmos/evm/pull/443) Move `ante` logic from the `evmd` Go package to the `evm` package to
be exported as a library.
- [\#422](https://github.com/cosmos/evm/pull/422) Align function and package names for consistency.
- [\#305](https://github.com/cosmos/evm/pull/305) Remove evidence precompile due to lack of use cases
