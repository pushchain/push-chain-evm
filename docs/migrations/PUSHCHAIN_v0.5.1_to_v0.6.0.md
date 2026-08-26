# push-chain-evm — cosmos/evm v0.5.1 → v0.6.0 Upgrade

This document describes how `push-chain-evm` (a heavily customized fork of
[`cosmos/evm`](https://github.com/cosmos/evm)) was upgraded from the **v0.5.1**
baseline to **v0.6.0**, how every upstream breaking change was absorbed, and —
most importantly — **how the push-chain–specific functionality was preserved**.

> **Upstream reference:** the official cosmos/evm migration guide,
> *Migration v0.5 → v0.6*:
> <https://cosmos-docs.mintlify.app/evm/latest/documentation/migrations/migration-v0.5-to-v0.6>
>
> Note: that page tracks the *latest* SDK/ibc-go line and refers to **ibc-go v11**.
> cosmos/evm **v0.6.0 itself** still pins **ibc-go v10**, so this fork stays on
> `github.com/cosmos/ibc-go/v10`. push-chain also pins **cosmos-sdk v0.50.x**
> (see [§7](#7-cosmos-sdk-v050-compatibility-the-overrideevents-shim)).

---

## 1. Merge strategy & fork relationship

`push-chain-evm`'s module path is `github.com/cosmos/evm`; it is a direct fork.
The relevant commit graph:

```
v0.5.1 (e2f6e47c)  ──┬── 208 push-chain commits ──▶ HEAD (96231e7a)   [the fork]
                     └── 2 commits ──────────────▶ v0.6.0 (6876c46d)  [upstream]
```

* `merge-base(HEAD, v0.6.0) == v0.5.1`, so the upgrade is a clean three-way merge:
  the upstream **v0.5.1 → v0.6.0 diff** is applied on top of the fork.
* `cosmos/evm` was added as the `upstream` remote and v0.6.0 fetched, then:

```bash
git remote add upstream https://github.com/cosmos/evm.git
git fetch upstream --tags
git merge v0.6.0      # left UNCOMMITTED on branch evm-upgrade-0.6.0
```

The v0.5.1→v0.6.0 delta touches ~101 files. Git auto-merged all but **3**; the
auto-merges were then verified semantically (see [§6](#6-auto-merged-files--semantic-verification)).

---

## 2. What v0.6.0 changes (the breaking surface)

| Area | Change |
|---|---|
| EVM execution API | `CallEVM`, `CallEVMWithData`, `ApplyMessage`, `ApplyMessageWithConfig` now take an explicit `stateDB *statedb.StateDB` (2nd param) and a `callFromPrecompile bool` (after `commit`). The caller now **owns** the StateDB; passing `nil` returns the new `ErrNilStateDB`. |
| StateDB | `CommitWithCacheCtx` → **`FlushToCacheCtx`**; `AddPrecompileFn`/`RevertMultiStore` drop their `events` argument; new `processedEventsCount` + `MarkEventProcessed`/`IsEventProcessed`; precompile-call event revert now uses `EventManager().OverrideEvents(...)`. |
| Custom IBC transfer | The bundled **`x/ibc/transfer`** module is **deleted**. ERC-20 ↔ IBC conversion moves to `x/erc20/keeper/convert.go` + the `erc20` IBC middleware + the ICS20 precompile. |
| ICS20 precompile | `ics20.NewPrecompile(...)` gains an `erc20Keeper cmn.ERC20Keeper` parameter. |
| `cmn.ERC20Keeper` | Gains `IsERC20Enabled`, `GetTokenPairID`, `ConvertERC20IntoCoinsForNativeToken(...)`. |
| Errors | New `types.ErrNilStateDB` (`"stateDB cannot be nil"`). |
| Dependencies | cosmos-sdk → v0.53.6, cometbft → v0.38.21, ledger-cosmos-go → v1.0.0, viper → v1.21.0, + `go.yaml.in/yaml/v3`; the `retract` block expands. |

---

## 3. Conflict resolutions (3 files)

### 3.1 `x/vm/keeper/call_evm.go` — the central conflict

This file contains both push-chain's **derived-transaction** engine *and* the
upstream `CallEVM`/`CallEVMWithData` whose signatures changed. Resolution:

* **Function signatures** were taken from v0.6.0 (`stateDB` + `callFromPrecompile`).
* **`CallEVMWithData`** was resolved to a **unified** body that honors *both*
  contracts:
  * It passes the caller-supplied `stateDB` straight through, so the v0.6.0
    nil-StateDB contract (`ErrNilStateDB`) is preserved — this is required by
    upstream tests (`TestCallEVM`/`TestCallEVMWithData/…nil statedb`).
  * It keeps push-chain's audit fix
    (`a05ad85a — "use cache context in CallEVMWithData to prevent gas meter
    overflow on revert"`) by **not** calling
    `ResetGasMeterAndConsumeGas(ctx, ctx.GasMeter().Limit())` on a revert
    (which can overflow the parent gas meter). In v0.6.0 the EVM already rolls
    back the reverted call frame via its own snapshot, so the previous
    cache-context wrap is no longer needed:

  ```go
  res, err := k.ApplyMessage(ctx, stateDB, msg, nil, commit, callFromPrecompile, true)
  if err != nil {
      return nil, err
  }
  if res.Failed() {
      // push-chain audit fix: do NOT ResetGasMeterAndConsumeGas here.
      return res, errorsmod.Wrap(types.ErrVMExecution, res.VmError)
  }
  ctx.GasMeter().ConsumeGas(res.GasUsed, "apply evm message")
  return res, nil
  ```

* **`DerivedEVMCallWithData`** (push-chain) was adapted to the new
  `ApplyMessageWithConfig` signature by creating its StateDB on the cache
  context (`callFromPrecompile=false`, derived txs are not precompile calls):

  ```go
  stateDB := statedb.New(tmpCtx, &k, txConfig)
  res, err := k.ApplyMessageWithConfig(tmpCtx, stateDB, msg, nil, commit, false, cfg, txConfig, true, nil)
  ```

  All derived-tx semantics — the cache-context commit, ABCI event emission
  (`ethereum_tx`/`tx_log`/`message`), the shared monotonic eth-tx index, the
  gasless gas reporting, and the reverted-execution bloom/event guards — are
  unchanged.

### 3.2 `x/ibc/transfer/module.go` — modify/delete

v0.6.0 deletes the entire custom transfer module; push-chain had only added a
`ConsensusVersion() = 5` override to it. The deletion was **accepted**
(`git rm -r x/ibc/transfer`) because v0.6.0 rewires every consumer
(`evmd/app.go`, `interfaces.go`, `precompiles/types/*`, `x/erc20/keeper/keeper.go`)
to the standard ibc-go transfer module + erc20 middleware. (The downstream
consequence of the `ConsensusVersion 5 → 6` change is documented in the
push-chain upgrade doc.)

### 3.3 `Makefile` — systemtests legacy binary

Base v0.5.1 built a `v0.4` legacy binary; v0.6.0 changed `test-system` to build a
`v0.5` binary via `git checkout v0.5.1`. push-chain had previously replaced that
fragile `git checkout` (which dirties `go.mod`) with an **isolated git worktree**
build (audit/CI fix `db3fbd13`). The resolution keeps push-chain's robust
worktree approach but retargets it to **v0.5.1** (`build-v05`), matching the
merged `tests/systemtests/upgrade_test.go` which now tests the
`v0.5.0-to-v0.6.0` upgrade and expects `binaries/v0.5/evmd`.

---

## 4. push-chain customizations preserved (verified)

| Feature | Where | Status |
|---|---|---|
| **Derived transactions** (`DerivedEVMCall` / `DerivedEVMCallWithData`) | `x/vm/keeper/call_evm.go` | Preserved; adapted to new `ApplyMessageWithConfig` signature. |
| **Derived-tx tracing** (`isUnsigned`, `unsignedTxAsMessage`, `from` param on `traceTx`/`TraceBlock`) | `x/vm/keeper/grpc_query.go` | Preserved (auto-merged with v0.6.0's per-call StateDB creation). |
| **baseFeeBurn** — `RefundGas(ctx, msg, leftoverGas, gasUsed, baseFee, denom)` burns `baseFee × gasUsed` | `x/vm/keeper/gas.go` + call in `state_transition.go` | Preserved. |
| **KV indexer derived-tx support** | `indexer/kv_indexer.go` | Preserved (untouched by v0.6.0). |
| **Derived-tx JSON-RPC** (receipts, logs, gas, tx_info) | `rpc/backend/*`, `rpc/types/*`, `rpc/namespaces/ethereum/eth/api.go` | Preserved (untouched by v0.6.0). |
| **CallEVMWithData gas-overflow audit fix** | `x/vm/keeper/call_evm.go` | Preserved (re-expressed for v0.6.0 — see §3.1). |
| **Audit fixes** F-2026-17736 / 17738 / 17740 / 17745 / 17752 / 17754 / 17775 | derived-tx + KV-indexer + RPC code | Preserved; regression tests pass. |
| **Param migration** (`v2/migrate_params.go`, `migrator.go`) | `x/vm/migrations/...` | Preserved. |
| **statedb nil-EventManager guard** | `x/vm/statedb/statedb.go` `New()` | Preserved (merged ahead of v0.6.0's `processedEventsCount` init, which would otherwise dereference a nil EventManager). |

---

## 5. push-chain compatibility patches added during the upgrade

### 5.1 `(*Keeper).NewStateDB(ctx)` — `x/vm/keeper/statedb.go`

A small convenience that returns `statedb.New(ctx, k, statedb.NewEmptyTxConfig())`.
It lets external modules that consume the EVM keeper **through an interface**
(e.g. push-chain's `x/uexecutor`) construct the now-mandatory non-nil StateDB
without exposing all 12 `statedb.Keeper` methods on their interface.

### 5.2 `overrideEventManagerEvents` shim — `x/vm/statedb/journal.go`

See [§7](#7-cosmos-sdk-v050-compatibility-the-overrideevents-shim).

### 5.3 Test coin-config nil-guard — `x/vm/types/denom_config_testing.go`

The `-tags=test` coin-denom getters now fall back to a non-nil zero value when
the global `testingEvmCoinInfo` is momentarily nil. This mirrors the production
`getEvmCoinInfo()` nil-guard (CHANGELOG #816) and fixes a test-harness race
(see [§8](#8-testing--results)).

### 5.4 `TestRefundGas/invalid GasPrice` fix — `tests/integration/x/vm/test_state_transition.go`

A pre-existing push-chain test bug: it passed `leftoverGas = 0`, so
`remaining = leftoverGas × gasPrice = 0` and the negative-price error path was
never hit. Changed to `leftoverGas = params.TxGas` (with `expGasRefund = 0`) so
the case actually exercises the error. (Verified failing identically on
pre-merge HEAD.)

---

## 6. Auto-merged files — semantic verification

These were auto-merged by git and then verified to contain **both** sides:

* `x/vm/keeper/grpc_query.go` — v0.6.0 adds `statedb.New(...)` before every
  `ApplyMessageWithConfig` call (note the value vs. pointer receiver:
  `statedb.New(ctx, &k, …)` in `EthCall`/`EstimateGasInternal`/predecessor loop,
  `statedb.New(ctx, k, …)` in `traceTxWithMsg`); push-chain's derived-tx tracing
  helpers and the `from common.Address` parameter are intact.
* `x/vm/keeper/state_transition.go` — v0.6.0's StateDB creation in
  `ApplyTransaction` + the new `ApplyMessage(WithConfig)` signatures, alongside
  push-chain's `RefundGas(..., res.GasUsed, cfg.BaseFee, ...)` baseFeeBurn call.
* `x/vm/statedb/statedb.go` — push-chain's nil-EventManager guard +
  v0.6.0's `processedEventsCount`/`FlushToCacheCtx`.
* `x/vm/types/errors.go` — push-chain's raw-bytes `RevertError` +
  v0.6.0's `ErrNilStateDB`.
* `go.mod` / `go.sum` / `evmd/go.mod` / `evmd/go.sum` — v0.6.0's dependency bumps
  + push-chain's goja/regexp2/sourcemap (JS tracer) indirect deps; reconciled
  with `go mod tidy`.

The v0.6.0-only files that push-chain never modified were taken wholesale and
already use the new APIs: `x/erc20/keeper/{evm,msg_server,convert,ibc_callbacks}.go`,
`x/ibc/callbacks/keeper/keeper.go`, `precompiles/common/{precompile,balance_handler,interfaces}.go`,
`precompiles/ics20/*`, `evmd/app.go`, `interfaces.go`, `precompiles/types/*`.

---

## 7. cosmos-sdk v0.50 compatibility: the `OverrideEvents` shim

v0.6.0's precompile-call event revert calls
`s.cacheCtx.EventManager().OverrideEvents(pc.prevEvents)` in
`x/vm/statedb/journal.go`. **`EventManager.OverrideEvents` only exists in
cosmos-sdk v0.53**, but push-chain pins **cosmos-sdk v0.50.x** for the whole
application (the rest of v0.6.0 is v0.50-compatible).

Bumping the parent's SDK constellation was rejected as out of scope — it would
cascade (`cosmossdk.io/collections v0.4 → v1.3`, `api v0.7.5 → v0.9.2`,
`x/tx v0.13 → v0.14`, …). Instead, a behavior-identical in-place shim was added:

```go
// x/vm/statedb/journal.go
func overrideEventManagerEvents(em sdk.EventManagerI, events sdk.Events) {
    f := reflect.ValueOf(em).Elem().FieldByName("events")
    reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Set(reflect.ValueOf(events))
}
```

**Why in-place (and not a fresh-EventManager swap):** precompiles obtain the
cache context via `stateDB.GetCacheContext()`, which shares the *same*
`EventManager` pointer as `stateDB.cacheCtx`. `OverrideEvents` mutates that
manager in place, so all holders observe the revert. A swap
(`cacheCtx.WithEventManager(new)`) only updates `stateDB.cacheCtx` and diverges
from outstanding copies — this was confirmed to break
`x/vm/statedb/balance_events_test.go` and nested-precompile reverts. The shim
reproduces `OverrideEvents` exactly and works on both v0.50 and v0.53.

> If push-chain ever moves to cosmos-sdk v0.53, revert this shim to the upstream
> `s.cacheCtx.EventManager().OverrideEvents(pc.prevEvents)` call.

---

## 8. Testing & results

Standalone builds and tests use the cosmos/evm `test` build tag:

```bash
go build ./...                 # root module — clean
cd evmd && go build ./...      # evmd example app — clean
go test -tags=test ./...       # tests
```

**Passing** (push-chain-critical):

* `indexer` (derived KV-indexer), `rpc/backend` (derived logs/gas/tracing/tx_info),
  `x/vm/statedb` (incl. the precompile event-revert path through the shim).
* `evmd` `TestKeeperTestSuite` — all `TestDerivedEVMCall*`, `TestRefundGas`
  (baseFeeBurn), `TestApplyMessage*` incl. the nil-StateDB cases,
  `TestCallEVM`/`TestCallEVMWithData` incl. the nil-StateDB cases.
* `evmd` ERC20 keeper + ERC20 **precompile** integration (the
  `callFromPrecompile=true` path), IBC callbacks, mempool, and the full
  precompile suite.

**Pre-existing failures (NOT introduced by this upgrade)** — each verified to
fail *identically* on pre-merge HEAD (`96231e7a`):

* `evmd/.../precompiles/bank` `TestBankPrecompileIntegrationTestSuite`
  (totalSupply / supplyOf). The queries are sent as **gas-paying transactions**;
  push-chain's **baseFeeBurn** burns base fees and lowers total supply below the
  upstream test's hard-coded expectation. (Fixing it would require teaching that
  upstream test about baseFeeBurn.)
* `evmd/.../integration` `TestBackend/TestGetTransactionReceipt/success_-_tx_not_found`
  — a pre-existing backend mock expectation.

A mempool-test regression (`evmd/.../integration/mempool`) **was** introduced and
**fixed**: a leaked `legacypool` reorg goroutine of a torn-down test network read
the global `testingEvmCoinInfo` during a between-test reset nil-window (exposed by
v0.6.0 timing). Resolved by the test-coin-config nil-guard (§5.3); the suite now
passes deterministically.

---

## 9. State of the repository

* The merge is **staged but not committed** on branch `evm-upgrade-0.6.0`
  (`git status` shows "All conflicts fixed but you are still merging"). Run
  `git commit` to conclude it.
* The `upstream` remote (`github.com/cosmos/evm`) is retained.
* No state/store migration is required by the evm modules themselves: none of
  `x/vm`, `x/erc20`, `x/feemarket`, `x/precisebank` bumped its `ConsensusVersion`
  (all remain at their prior values); no store keys were added or removed.
