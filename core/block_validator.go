// Copyright 2015 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package core

import (
	"fmt"

	"github.com/dominant-strategies/go-quai/common"
	"github.com/dominant-strategies/go-quai/consensus"
	"github.com/dominant-strategies/go-quai/core/state"
	"github.com/dominant-strategies/go-quai/core/types"
	"github.com/dominant-strategies/go-quai/params"
	"github.com/dominant-strategies/go-quai/trie"
)

// BlockValidator is responsible for validating block headers, uncles and
// processed state.
//
// BlockValidator implements Validator.
type BlockValidator struct {
	config *params.ChainConfig // Chain configuration options
	hc     *HeaderChain        // HeaderChain
	engine consensus.Engine    // Consensus engine used for validating
}

// NewBlockValidator returns a new block validator which is safe for re-use
func NewBlockValidator(config *params.ChainConfig, headerChain *HeaderChain, engine consensus.Engine) *BlockValidator {
	validator := &BlockValidator{
		config: config,
		engine: engine,
		hc:     headerChain,
	}
	return validator
}

// ValidateBody validates the given block's uncles and verifies the block
// header's transaction and uncle roots. The headers are assumed to be already
// validated at this point.
func (v *BlockValidator) ValidateBody(block *types.Block) error {
	nodeCtx := common.NodeLocation.Context()
	// Check whether the block's known, and if not, that it's linkable
	if v.hc.bc.processor.HasBlockAndState(block.Hash(), block.NumberU64()) {
		return ErrKnownBlock
	}
	// Header validity is known at this point, check the uncles and transactions
	header := block.Header()
	if err := v.engine.VerifyUncles(v.hc, block); err != nil {
		return err
	}
	if hash := types.CalcUncleHash(block.Uncles()); hash != header.UncleHash() {
		return fmt.Errorf("uncle root hash mismatch: have %x, want %x", hash, header.UncleHash())
	}
	if hash := types.DeriveSha(block.Transactions(), trie.NewStackTrie(nil)); hash != header.TxHash() {
		return fmt.Errorf("transaction root hash mismatch: have %x, want %x", hash, header.TxHash())
	}
	if hash := types.DeriveSha(block.ExtTransactions(), trie.NewStackTrie(nil)); hash != header.EtxHash() {
		return fmt.Errorf("external transaction root hash mismatch: have %x, want %x", hash, header.EtxHash())
	}
	// Subordinate manifest must match ManifestHash in subordinate context, _iff_
	// we have a subordinate (i.e. if we are not a zone)
	if nodeCtx < common.ZONE_CTX {
		subManifestHash := types.DeriveSha(block.SubManifest(), trie.NewStackTrie(nil))
		if subManifestHash == types.EmptyRootHash || subManifestHash != header.ManifestHash(nodeCtx+1) {
			// If we have a subordinate chain, it is impossible for the subordinate manifest to be empty
			return ErrBadSubManifest
		}
	}
	if !v.hc.bc.processor.HasBlockAndState(block.ParentHash(), block.NumberU64()-1) {
		if !v.hc.bc.HasBlock(block.ParentHash(), block.NumberU64()-1) {
			return consensus.ErrUnknownAncestor
		}
		return consensus.ErrPrunedAncestor
	}
	return nil
}

// ValidateState validates the various changes that happen after a state
// transition, such as amount of used gas, the receipt roots and the state root
// itself. ValidateState returns a database batch if the validation was a success
// otherwise nil and an error is returned.
func (v *BlockValidator) ValidateState(block *types.Block, statedb *state.StateDB, receipts types.Receipts, usedGas uint64) error {
	header := block.Header()
	if block.GasUsed() != usedGas {
		return fmt.Errorf("invalid gas used (remote: %d local: %d)", block.GasUsed(), usedGas)
	}
	// Validate the received block's bloom with the one derived from the generated receipts.
	// For valid blocks this should always validate to true.
	rbloom := types.CreateBloom(receipts)
	if rbloom != header.Bloom() {
		return fmt.Errorf("invalid bloom (remote: %x  local: %x)", header.Bloom(), rbloom)
	}
	// Tre receipt Trie's root (R = (Tr [[H1, R1], ... [Hn, Rn]]))
	receiptSha := types.DeriveSha(receipts, trie.NewStackTrie(nil))
	if receiptSha != header.ReceiptHash() {
		return fmt.Errorf("invalid receipt root hash (remote: %x local: %x)", header.ReceiptHash(), receiptSha)
	}
	// Validate the state root against the received state root and throw
	// an error if they don't match.
	if root, err := statedb.IntermediateRoot(v.config.IsEIP158(header.Number())); header.Root() != root || err != nil {
		if err != nil {
			return err
		}
		return fmt.Errorf("invalid merkle root (remote: %x local: %x)", header.Root(), root)
	}
	// Collect ETXs emitted from each successful transaction
	var emittedEtxs types.Transactions
	for _, receipt := range receipts {
		if receipt.Status == types.ReceiptStatusSuccessful {
			for _, etx := range receipt.Etxs {
				emittedEtxs = append(emittedEtxs, etx)
			}
		}
	}
	// Confirm the ETXs emitted by the transactions in this block exactly match the
	// ETXs given in the block body
	if etxHash := types.DeriveSha(emittedEtxs, trie.NewStackTrie(nil)); etxHash != header.EtxHash() {
		return fmt.Errorf("invalid etx hash (remote: %x local: %x)", header.EtxHash(), etxHash)
	}
	// Collect the ETX rollup with emitted ETXs since the last coincident block,
	// excluding this block.
	etxRollup, err := v.hc.CollectEtxRollup(block)
	if err != nil {
		return fmt.Errorf("unable to get ETX rollup")
	}
	if etxRollupHash := types.DeriveSha(etxRollup, trie.NewStackTrie(nil)); etxRollupHash != header.EtxRollupHash() {
		return fmt.Errorf("invalid etx rollup hash (remote: %x local: %x)", header.EtxRollupHash(), etxRollupHash)
	}
	return nil
}

// CalcGasLimit computes the gas limit of the next block after parent. It aims
// to keep the baseline gas close to the provided target, and increase it towards
// the target if the baseline gas is lower.
func CalcGasLimit(parentGasLimit, desiredLimit uint64) uint64 {
	delta := parentGasLimit/params.GasLimitBoundDivisor - 1
	limit := parentGasLimit
	if desiredLimit < params.MinGasLimit {
		desiredLimit = params.MinGasLimit
	}
	// If we're outside our allowed gas range, we try to hone towards them
	if limit < desiredLimit {
		limit = parentGasLimit + delta
		if limit > desiredLimit {
			limit = desiredLimit
		}
		return limit
	}
	if limit > desiredLimit {
		limit = parentGasLimit - delta
		if limit < desiredLimit {
			limit = desiredLimit
		}
	}
	return limit
}
