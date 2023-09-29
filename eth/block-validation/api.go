package blockvalidation

import (
	"encoding/json"
	"errors"
	"fmt"

	builderApiBellatrix "github.com/attestantio/go-builder-client/api/bellatrix"
	builderApiCapella "github.com/attestantio/go-builder-client/api/capella"
	builderApiDeneb "github.com/attestantio/go-builder-client/api/deneb"
	builderApiV1 "github.com/attestantio/go-builder-client/api/v1"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/rpc"
)

// Register adds catalyst APIs to the full node.
func Register(stack *node.Node, backend *eth.Ethereum) error {
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
func NewBlockValidationAPI(eth *eth.Ethereum, accessVerifier *AccessVerifier, useBalanceDiffProfit bool) *BlockValidationAPI {
	return &BlockValidationAPI{
		eth:                  eth,
		useBalanceDiffProfit: useBalanceDiffProfit,
	}
}

type BuilderBlockValidationRequest struct {
	builderApiBellatrix.SubmitBlockRequest
	RegisteredGasLimit uint64 `json:"registered_gas_limit,string"`
}

func (api *BlockValidationAPI) ValidateBuilderSubmissionV1(params *BuilderBlockValidationRequest) error {
	// no longer supported endpoint
	if params.ExecutionPayload == nil {
		return errors.New("nil execution payload")
	}
	payload := params.ExecutionPayload
	block, err := engine.ExecutionPayloadV1ToBlock(payload)
	if err != nil {
		return err
	}

	if params.Message.ParentHash != boostTypes.Hash(block.ParentHash()) {
		return fmt.Errorf("incorrect ParentHash %s, expected %s", params.Message.ParentHash.String(), block.ParentHash().String())
	}

	if params.Message.BlockHash != boostTypes.Hash(block.Hash()) {
		return fmt.Errorf("incorrect BlockHash %s, expected %s", params.Message.BlockHash.String(), block.Hash().String())
	}

	if params.Message.GasLimit != block.GasLimit() {
		return fmt.Errorf("incorrect GasLimit %d, expected %d", params.Message.GasLimit, block.GasLimit())
	}

	if params.Message.GasUsed != block.GasUsed() {
		return fmt.Errorf("incorrect GasUsed %d, expected %d", params.Message.GasUsed, block.GasUsed())
	}

	feeRecipient := common.BytesToAddress(params.Message.ProposerFeeRecipient[:])
	expectedProfit := params.Message.Value.BigInt()

	var vmconfig vm.Config

	err = api.eth.BlockChain().ValidatePayload(block, feeRecipient, expectedProfit, params.RegisteredGasLimit, vmconfig)
	if err != nil {
		log.Error("invalid payload", "hash", payload.BlockHash.String(), "number", payload.BlockNumber, "parentHash", payload.ParentHash.String(), "err", err)
		return err
	}

	log.Info("validated block", "hash", block.Hash(), "number", block.NumberU64(), "parentHash", block.ParentHash())
	return nil
}

type BuilderBlockValidationRequestV2 struct {
	builderApiCapella.SubmitBlockRequest
	RegisteredGasLimit uint64 `json:"registered_gas_limit,string"`
}

func (r *BuilderBlockValidationRequestV2) UnmarshalJSON(data []byte) error {
	params := &struct {
		RegisteredGasLimit uint64 `json:"registered_gas_limit,string"`
	}{}
	err := json.Unmarshal(data, params)
	if err != nil {
		return err
	}
	r.RegisteredGasLimit = params.RegisteredGasLimit

	blockRequest := new(builderApiCapella.SubmitBlockRequest)
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

	return api.validateBlock(block, params.Message, params.RegisteredGasLimit)
}

type BuilderBlockValidationRequestV3 struct {
	builderApiDeneb.SubmitBlockRequest
	ParentBeaconBlockRoot common.Hash `json:"parent_beacon_block_root"`
	RegisteredGasLimit    uint64      `json:"registered_gas_limit,string"`
}

func (r *BuilderBlockValidationRequestV3) UnmarshalJSON(data []byte) error {
	params := &struct {
		ParentBeaconBlockRoot common.Hash `json:"parent_beacon_block_root"`
		RegisteredGasLimit    uint64      `json:"registered_gas_limit,string"`
	}{}
	err := json.Unmarshal(data, params)
	if err != nil {
		return err
	}
	r.RegisteredGasLimit = params.RegisteredGasLimit
	r.ParentBeaconBlockRoot = params.ParentBeaconBlockRoot

	blockRequest := new(builderApiDeneb.SubmitBlockRequest)
	err = json.Unmarshal(data, &blockRequest)
	if err != nil {
		return err
	}
	r.SubmitBlockRequest = *blockRequest
	return nil
}

func (api *BlockValidationAPI) ValidateBuilderSubmissionV3(params *BuilderBlockValidationRequestV3) error {
	// TODO: fuzztest, make sure the validation is sound
	payload := params.ExecutionPayload
	blobsBundle := params.BlobsBundle
	log.Info("blobs bundle", "blobs", len(blobsBundle.Blobs), "commits", len(blobsBundle.Commitments), "proofs", len(blobsBundle.Proofs))
	block, err := engine.ExecutionPayloadV3ToBlock(payload, blobsBundle, params.ParentBeaconBlockRoot)
	if err != nil {
		return err
	}

	err = api.validateBlock(block, params.Message, params.RegisteredGasLimit)
	if err != nil {
		log.Error("invalid payload", "hash", block.Hash, "number", block.NumberU64(), "parentHash", block.ParentHash, "err", err)
		return err
	}
	err = validateBlobsBundle(block.Transactions(), blobsBundle)
	if err != nil {
		log.Error("invalid blobs bundle", "err", err)
		return err
	}
	return nil
}

func (api *BlockValidationAPI) validateBlock(block *types.Block, msg *builderApiV1.BidTrace, registeredGasLimit uint64) error {
	if msg.ParentHash != phase0.Hash32(block.ParentHash()) {
		return fmt.Errorf("incorrect ParentHash %s, expected %s", msg.ParentHash.String(), block.ParentHash().String())
	}

	if msg.BlockHash != phase0.Hash32(block.Hash()) {
		return fmt.Errorf("incorrect BlockHash %s, expected %s", msg.BlockHash.String(), block.Hash().String())
	}

	if msg.GasLimit != block.GasLimit() {
		log.Error("incorrect GasLimit", "got", params.Message.GasLimit, "expected", block.GasLimit())
		return fmt.Errorf("incorrect GasLimit %d, expected %d", msg.GasLimit, block.GasLimit())
	}

	if msg.GasUsed != block.GasUsed() {
		return fmt.Errorf("incorrect GasUsed %d, expected %d", msg.GasUsed, block.GasUsed())
	}

	feeRecipient := common.BytesToAddress(msg.ProposerFeeRecipient[:])
	expectedProfit := msg.Value.ToBig()

	var vmconfig vm.Config

	err := api.eth.BlockChain().ValidatePayload(block, feeRecipient, expectedProfit, registeredGasLimit, vmconfig, api.useBalanceDiffProfit)
	if err != nil {
		return err
	}

	log.Info("validated block", "hash", block.Hash(), "number", block.NumberU64(), "parentHash", block.ParentHash())
	return nil
}

func validateBlobsBundle(txs types.Transactions, blobsBundle *builderApiDeneb.BlobsBundle) error {
	var hashes []common.Hash
	for _, tx := range txs {
		hashes = append(hashes, tx.BlobHashes()...)
	}
	blobs := blobsBundle.Blobs
	commits := blobsBundle.Commitments
	proofs := blobsBundle.Proofs

	if len(blobs) != len(hashes) {
		return fmt.Errorf("invalid number of %d blobs compared to %d blob hashes", len(blobs), len(hashes))
	}
	if len(commits) != len(hashes) {
		return fmt.Errorf("invalid number of %d blob commitments compared to %d blob hashes", len(commits), len(hashes))
	}
	if len(proofs) != len(hashes) {
		return fmt.Errorf("invalid number of %d blob proofs compared to %d blob hashes", len(proofs), len(hashes))
	}

	for i := range blobs {
		if err := kzg4844.VerifyBlobProof(kzg4844.Blob(blobs[i]), kzg4844.Commitment(commits[i]), kzg4844.Proof(proofs[i])); err != nil {
			return fmt.Errorf("invalid blob %d: %v", i, err)
		}
	}
	log.Info("validated blobs bundle", "blobs", len(blobs), "commits", len(commits), "proofs", len(proofs))
	return nil
}
