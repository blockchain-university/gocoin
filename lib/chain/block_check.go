package chain

import (
	"fmt"
	"time"
	"bytes"
	"errors"
	"encoding/binary"
	"github.com/piotrnar/gocoin/lib/btc"
	"github.com/piotrnar/gocoin/lib/script"
)

func (ch *Chain) PreCheckBlock(bl *btc.Block) (er error, dos bool, maybelater bool) {
	// Size limits
	if len(bl.Raw)<81 || len(bl.Raw)>btc.MAX_BLOCK_SIZE && bl.SerializedSize()>btc.MAX_BLOCK_SIZE {
		er = errors.New("CheckBlock() : size limits failed - RPC_Result:bad-blk-length")
		dos = true
		return
	}

	if bl.Version()==0 {
		er = errors.New("CheckBlock() : Block version 0 not allowed - RPC_Result:bad-version")
		dos = true
		return
	}

	// Check proof-of-work
	if !btc.CheckProofOfWork(bl.Hash, bl.Bits()) {
		er = errors.New("CheckBlock() : proof of work failed - RPC_Result:high-hash")
		dos = true
		return
	}

	// Check timestamp (must not be higher than now +2 hours)
	if int64(bl.BlockTime()) > time.Now().Unix() + 2 * 60 * 60 {
		er = errors.New("CheckBlock() : block timestamp too far in the future - RPC_Result:time-too-new")
		dos = true
		return
	}

	if prv, pres := ch.BlockIndex[bl.Hash.BIdx()]; pres {
		if prv.Parent == nil {
			// This is genesis block
			er = errors.New("Genesis")
			return
		} else {
			er = errors.New("CheckBlock: "+bl.Hash.String()+" already in - RPC_Result:duplicate")
			return
		}
	}

	prevblk, ok := ch.BlockIndex[btc.NewUint256(bl.ParentHash()).BIdx()]
	if !ok {
		er = errors.New("CheckBlock: "+bl.Hash.String()+" parent not found - RPC_Result:bad-prevblk")
		maybelater = true
		return
	}

	bl.Height = prevblk.Height+1

	// Reject the block if it reaches into the chain deeper than our unwind buffer
	if prevblk!=ch.BlockTreeEnd && int(ch.BlockTreeEnd.Height)-int(bl.Height)>=MovingCheckopintDepth {
		er = errors.New(fmt.Sprint("CheckBlock: btc.Block ", bl.Hash.String(),
			" hooks too deep into the chain: ", bl.Height, "/", ch.BlockTreeEnd.Height, " ",
			btc.NewUint256(bl.ParentHash()).String(), " - RPC_Result:bad-prevblk"))
		return
	}

	// Check proof of work
	gnwr := ch.GetNextWorkRequired(prevblk, bl.BlockTime())
	if bl.Bits() != gnwr {
		er = errors.New("CheckBlock: incorrect proof of work - RPC_Result:bad-diffbits")
		dos = true
		return
	}

	// Check timestamp against prev
	if bl.BlockTime() <= prevblk.GetMedianTimePast() {
		er = errors.New("CheckBlock: block's timestamp is too early - RPC_Result:time-too-old")
		dos = true
		return
	}

	// Count block versions within the Majority Window
	n := prevblk
	for cnt:=uint(0); cnt<ch.Consensus.Window && n!=nil; cnt++ {
		ver := binary.LittleEndian.Uint32(n.BlockHeader[0:4])
		if ver >= 2 {
			bl.Majority_v2++
			if ver >= 3 {
				bl.Majority_v3++
				if ver >= 4 {
					bl.Majority_v4++
				}
			}
		}
		n = n.Parent
	}

	if bl.Version()<2 && bl.Majority_v2>=ch.Consensus.RejectBlock {
		er = errors.New("CheckBlock() : Rejected nVersion=1 block - RPC_Result:bad-version")
		dos = true
		return
	}

	if bl.Version()<3 && bl.Majority_v3>=ch.Consensus.RejectBlock {
		er = errors.New("CheckBlock() : Rejected nVersion=2 block - RPC_Result:bad-version")
		dos = true
		return
	}

	if bl.Version()<4 && bl.Majority_v4>=ch.Consensus.RejectBlock {
		er = errors.New("CheckBlock() : Rejected nVersion=3 block - RPC_Result:bad-version")
		dos = true
		return
	}

	return
}


func (ch *Chain) PostCheckBlock(bl *btc.Block) (er error) {
	if bl.Txs==nil {
		er = bl.BuildTxList()
		if er != nil {
			return
		}
	}

	if !bl.Trusted {
		if bl.Version()>=2 && bl.Majority_v2>=ch.Consensus.EnforceUpgrade {
			var exp []byte
			if bl.Height >= 0x800000 {
				if bl.Height >= 0x80000000 {
					exp = []byte{5, byte(bl.Height), byte(bl.Height>>8), byte(bl.Height>>16), byte(bl.Height>>24), 0}
				} else {
					exp = []byte{4, byte(bl.Height), byte(bl.Height>>8), byte(bl.Height>>16), byte(bl.Height>>24)}
				}
			} else {
				exp = []byte{3, byte(bl.Height), byte(bl.Height>>8), byte(bl.Height>>16)}
			}
			if len(bl.Txs[0].TxIn[0].ScriptSig)<len(exp) || !bytes.Equal(exp, bl.Txs[0].TxIn[0].ScriptSig[:len(exp)]) {
				er = errors.New("CheckBlock() : Unexpected block number in coinbase: "+bl.Hash.String()+" - RPC_Result:bad-cb-height")
				return
			}
		}

		// We need to be satoshi compatible
		if len(bl.Txs)==0 || !bl.Txs[0].IsCoinBase() {
			er = errors.New("CheckBlock() : first tx is not coinbase: "+bl.Hash.String()+" - RPC_Result:bad-cb-missing")
			return
		}

		// And again...
		for i:=1; i<len(bl.Txs); i++ {
			if bl.Txs[i].IsCoinBase() {
				er = errors.New("CheckBlock() : more than one coinbase: "+bl.Hash.String()+" - RPC_Result:bad-cb-multiple")
				return
			}
		}

		// Check Merkle Root - that's importnant
		merkel, mutated := btc.GetMerkel(bl.Txs)
		if mutated {
			er = errors.New("CheckBlock(): duplicate transaction - RPC_Result:bad-txns-duplicate")
			return
		}

		if !bytes.Equal(merkel, bl.MerkleRoot()) {
			er = errors.New("CheckBlock() : Merkle Root mismatch - RPC_Result:bad-txnmrklroot")
			return
		}

		// Check transactions - this is the most time consuming task
		if !CheckTransactions(bl.Txs, bl.Height, bl.BlockTime()) {
			er = errors.New("CheckBlock() : CheckTransactions() failed - RPC_Result:bad-tx")
			return
		}
	}

	if bl.BlockTime()>=BIP16SwitchTime {
		bl.VerifyFlags = script.VER_P2SH
	} else {
		bl.VerifyFlags = 0
	}

	if bl.Majority_v3>=ch.Consensus.EnforceUpgrade {
		bl.VerifyFlags |= script.VER_DERSIG
	}

	if bl.Version()>=4 && bl.Majority_v4>=ch.Consensus.EnforceUpgrade {
		bl.VerifyFlags |= script.VER_CLTV
	}

	return
}


func (ch *Chain) CheckBlock(bl *btc.Block) (er error, dos bool, maybelater bool) {
	er, dos, maybelater = ch.PreCheckBlock(bl)
	if er == nil {
		er = ch.PostCheckBlock(bl)
		if er != nil { // all post-check errors are DoS kind
			dos = true
		}
	}
	return
}
