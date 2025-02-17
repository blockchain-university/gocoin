package btc

import (
	"errors"
	"encoding/binary"
	"github.com/piotrnar/gocoin/lib/others/sys"
)

type Block struct {
	Raw []byte
	Hash *Uint256
	Txs []*Tx
	TxCount, TxOffset int  // Number of transactions and byte offset to the first one
	Trusted bool // if the block is trusted, we do not check signatures and some other things...
	LastKnownHeight uint32

	// These flags are set during chain.(Pre/Post)CheckBlock and used later (e.g. by script.VerifyTxScript):
	VerifyFlags uint32
	Majority_v2, Majority_v3, Majority_v4 uint
	Height uint32
	Sigops uint32
}


func NewBlock(data []byte) (*Block, error) {
	if len(data)<81 {
		return nil, errors.New("Block too short")
	}

	var bl Block
	bl.Hash = NewSha2Hash(data[:80])
	bl.Raw = data
	bl.TxCount, bl.TxOffset = VLen(data[80:])
	if bl.TxOffset == 0 {
		return nil, errors.New("Block's txn_count field corrupt - RPC_Result:bad-blk-length")
	}
	bl.TxOffset += 80
	return &bl, nil
}


func (bl *Block)Version() uint32 {
	return binary.LittleEndian.Uint32(bl.Raw[0:4])
}

func (bl *Block)ParentHash() []byte {
	return bl.Raw[4:36]
}

func (bl *Block)MerkleRoot() []byte {
	return bl.Raw[36:68]
}

func (bl *Block)BlockTime() uint32 {
	return binary.LittleEndian.Uint32(bl.Raw[68:72])
}

func (bl *Block)Bits() uint32 {
	return binary.LittleEndian.Uint32(bl.Raw[72:76])
}


func (bl *Block) SerializedSize() int {
	cnt, off := VLen(bl.Raw[80:])
	return 80 + VLenSize(uint64(cnt)) + len(bl.Raw)-80-off
}



// Parses block's transactions and adds them to the structure, calculating hashes BTW.
// It would be more elegant to use bytes.Reader here, but this solution is ~20% faster.
func (bl *Block) BuildTxList() (e error) {
	if bl.TxCount==0 {
		bl.TxCount, bl.TxOffset = VLen(bl.Raw[80:])
		if bl.TxCount==0 || bl.TxOffset==0 {
			e = errors.New("Block's txn_count field corrupt - RPC_Result:bad-blk-length")
			return
		}
		bl.TxOffset += 80
	}
	bl.Txs = make([]*Tx, bl.TxCount)

	offs := bl.TxOffset

	done := make(chan bool, sys.UseThreads)
	for i:=0; i<sys.UseThreads; i++ {
		done <- false
	}

	for i:=0; i<bl.TxCount; i++ {
		var n int
		bl.Txs[i], n = NewTx(bl.Raw[offs:])
		if bl.Txs[i] == nil || n==0 {
			e = errors.New("NewTx failed")
			break
		}
		bl.Txs[i].Size = uint32(n)
		_ = <- done // wait here, if we have too many threads already
		go func(h **Uint256, b []byte) {
			*h = NewSha2Hash(b) // Calculate tx hash in a background
			done <- true // indicate mission completed
		}(&bl.Txs[i].Hash, bl.Raw[offs:offs+n])
		offs += n
	}

	for i := range bl.Txs[0].TxOut {
		bl.Txs[0].TxOut[i].WasCoinbase = true
	}

	// Wait for all the pending missions to complete...
	for i:=0; i<sys.UseThreads; i++ {
		_ = <- done
	}
	return
}


func (bl *Block) ComputeMerkel() (res []byte, mutated bool) {
	tx_cnt, offs := VLen(bl.Raw[80:])
	offs += 80

	mtr := make([][]byte, tx_cnt)

	done := make(chan bool, sys.UseThreads)
	for i:=0; i<sys.UseThreads; i++ {
		done <- false
	}

	for i:=0; i<tx_cnt; i++ {
		n := TxSize(bl.Raw[offs:])
		if n==0 {
			break
		}
		_ = <- done // wait here, if we have too many threads already
		go func(i int, b []byte) {
			mtr[i] = make([]byte, 32)
			ShaHash(b, mtr[i])
			done <- true // indicate mission completed
		}(i, bl.Raw[offs:offs+n])
		offs += n
	}

	// Wait for all the pending missions to complete...
	for i:=0; i<sys.UseThreads; i++ {
		_ = <- done
	}

	res, mutated = CalcMerkel(mtr)
	return
}


func GetBlockReward(height uint32) (uint64) {
	return 50e8 >> (height/210000)
}
