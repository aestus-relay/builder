package blockvalidation

import (
	"encoding/json"
	"errors"
	"fmt"

	bellatrixapi "github.com/attestantio/go-builder-client/api/bellatrix"
	capellaapi "github.com/attestantio/go-builder-client/api/capella"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/rpc"
)

type BlockValidationConfig struct {
	// If set to true, proposer payment is calculated as a balance difference of the fee recipient.
	UseBalanceDiffProfit bool
}

// Register adds catalyst APIs to the full node.
func Register(stack *node.Node, backend *eth.Ethereum, cfg BlockValidationConfig) error {
	stack.RegisterAPIs([]rpc.API{
		{
			Namespace: "flashbots",
			Service:   NewBlockValidationAPI(backend, cfg.UseBalanceDiffProfit),
		},
	})
	return nil
}

type BlockValidationAPI struct {
	eth            *eth.Ethereum
	// If set to true, proposer payment is calculated as a balance difference of the fee recipient.
	useBalanceDiffProfit bool
}

// NewConsensusAPI creates a new consensus api for the given backend.
// The underlying blockchain needs to have a valid terminal total difficulty set.
func NewBlockValidationAPI(eth *eth.Ethereum, useBalanceDiffProfit bool) *BlockValidationAPI {
	return &BlockValidationAPI{
		eth:                  eth,
		useBalanceDiffProfit: useBalanceDiffProfit,
	}
}

type BuilderBlockValidationRequest struct {
	bellatrixapi.SubmitBlockRequest
	RegisteredGasLimit uint64 `json:"registered_gas_limit,string"`
}

func (api *BlockValidationAPI) ValidateBuilderSubmissionV1(params *BuilderBlockValidationRequest) error {
	// TODO: fuzztest, make sure the validation is sound
	// TODO: handle context!

	if params.ExecutionPayload == nil {
		return errors.New("nil execution payload")
	}
	payload := params.ExecutionPayload
	block, err := engine.ExecutionPayloadToBlock(payload)
	if err != nil {
		return err
	}

	if params.Message.ParentHash != phase0.Hash32(block.ParentHash()) {
		return fmt.Errorf("incorrect ParentHash %s, expected %s", params.Message.ParentHash.String(), block.ParentHash().String())
	}

	if params.Message.BlockHash != phase0.Hash32(block.Hash()) {
		return fmt.Errorf("incorrect BlockHash %s, expected %s", params.Message.BlockHash.String(), block.Hash().String())
	}

	if params.Message.GasLimit != block.GasLimit() {
		return fmt.Errorf("incorrect GasLimit %d, expected %d", params.Message.GasLimit, block.GasLimit())
	}

	if params.Message.GasUsed != block.GasUsed() {
		return fmt.Errorf("incorrect GasUsed %d, expected %d", params.Message.GasUsed, block.GasUsed())
	}

	feeRecipient := common.BytesToAddress(params.Message.ProposerFeeRecipient[:])
	expectedProfit := params.Message.Value.ToBig()

	var vmconfig vm.Config

	err = api.eth.BlockChain().ValidatePayload(block, feeRecipient, expectedProfit, params.RegisteredGasLimit, vmconfig, api.useBalanceDiffProfit, params.Message.BuilderPubkey)
	if err != nil {
		log.Error("invalid payload", "hash", payload.BlockHash.String(), "number", payload.BlockNumber, "parentHash", payload.ParentHash.String(), "err", err)
		return err
	}

	log.Info("validated block", "hash", block.Hash(), "number", block.NumberU64(), "parentHash", block.ParentHash())
	return nil
}

type BuilderBlockValidationRequestV2 struct {
	capellaapi.SubmitBlockRequest
	RegisteredGasLimit uint64      `json:"registered_gas_limit,string"`
	WithdrawalsRoot    common.Hash `json:"withdrawals_root"`
}

func (r *BuilderBlockValidationRequestV2) UnmarshalJSON(data []byte) error {
	params := &struct {
		RegisteredGasLimit uint64      `json:"registered_gas_limit,string"`
		WithdrawalsRoot    common.Hash `json:"withdrawals_root"`
	}{}
	err := json.Unmarshal(data, params)
	if err != nil {
		return err
	}
	r.RegisteredGasLimit = params.RegisteredGasLimit
	r.WithdrawalsRoot = params.WithdrawalsRoot

	blockRequest := new(capellaapi.SubmitBlockRequest)
	err = json.Unmarshal(data, &blockRequest)
	if err != nil {
		return err
	}
	r.SubmitBlockRequest = *blockRequest
	return nil
}

func (api *BlockValidationAPI) ValidateBuilderSubmissionV2(params *BuilderBlockValidationRequestV2) error {
	// TODO: fuzztest, make sure the validation is sound
	// TODO: handle context!
	if params.ExecutionPayload == nil {
		log.Error("nil execution payload")
		return errors.New("nil execution payload")
	}
	payload := params.ExecutionPayload
	block, err := engine.ExecutionPayloadV2ToBlock(payload)
	if err != nil {
		log.Error("Could not convert payload to block", "err", err)
		return err
	}

	if params.Message.ParentHash != phase0.Hash32(block.ParentHash()) {
		log.Error("incorrect ParentHash", "got", params.Message.ParentHash.String(), "expected", block.ParentHash().String())
		return fmt.Errorf("incorrect ParentHash %s, expected %s", params.Message.ParentHash.String(), block.ParentHash().String())
	}

	if params.Message.BlockHash != phase0.Hash32(block.Hash()) {
		log.Error("incorrect BlockHash", "got", params.Message.BlockHash.String(), "expected", block.Hash().String())
		return fmt.Errorf("incorrect BlockHash %s, expected %s", params.Message.BlockHash.String(), block.Hash().String())
	}

	if params.Message.GasLimit != block.GasLimit() {
		log.Error("incorrect GasLimit", "got", params.Message.GasLimit, "expected", block.GasLimit())
		return fmt.Errorf("incorrect GasLimit %d, expected %d", params.Message.GasLimit, block.GasLimit())
	}

	if params.Message.GasUsed != block.GasUsed() {
		log.Error("incorrect GasUsed", "got", params.Message.GasUsed, "expected", block.GasUsed())
		return fmt.Errorf("incorrect GasUsed %d, expected %d", params.Message.GasUsed, block.GasUsed())
	}

	feeRecipient := common.BytesToAddress(params.Message.ProposerFeeRecipient[:])
	expectedProfit := params.Message.Value.ToBig()

	var vmconfig vm.Config

	bc := api.eth.BlockChain()
	err = bc.ValidatePayload(block, feeRecipient, expectedProfit, params.RegisteredGasLimit, vmconfig, api.useBalanceDiffProfit, params.Message.BuilderPubkey)
	if err != nil {
		log.Error("invalid payload", "hash", payload.BlockHash.String(), "number", payload.BlockNumber, "parentHash", payload.ParentHash.String(), "err", err)
		return err
	}

	log.Info("validated block", "hash", block.Hash(), "number", block.NumberU64(), "parentHash", block.ParentHash())
	return nil
}

