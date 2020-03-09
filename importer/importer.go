// Copyright (C) 2019-2020 Algorand, Inc.
// This file is part of the Algorand Indexer
//
// Algorand Indexer is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, either version 3 of the
// License, or (at your option) any later version.
//
// Algorand Indexer is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with Algorand Indexer.  If not, see <https://www.gnu.org/licenses/>.

package importer

import (
	"bytes"
	"fmt"

	"github.com/algorand/indexer/idb"
	"github.com/algorand/indexer/types"

	"github.com/algorand/go-algorand-sdk/encoding/msgpack"
)

type Importer interface {
	ImportBlock(blockbytes []byte) error
	ImportDecodedBlock(block *types.EncodedBlockCert) error
}

type dbImporter struct {
	db idb.IndexerDb
}

type stringInt struct {
	s string
	i int
}

var typeEnumList = []stringInt{
	{"pay", 1},
	{"keyreg", 2},
	{"acfg", 3},
	{"axfer", 4},
	{"afrz", 5},
}
var TypeEnumMap map[string]int

func init() {
	TypeEnumMap = make(map[string]int, len(typeEnumList))
	for _, si := range typeEnumList {
		TypeEnumMap[si.s] = si.i
	}
}

var zeroAddr = [32]byte{}

func participate(participants [][]byte, addr []byte) [][]byte {
	if len(addr) == 0 || bytes.Equal(addr, zeroAddr[:]) {
		return participants
	}
	for _, pp := range participants {
		if bytes.Equal(pp, addr) {
			return participants
		}
	}
	return append(participants, addr)
}

func (imp *dbImporter) ImportBlock(blockbytes []byte) (err error) {
	var blockContainer types.EncodedBlockCert
	err = msgpack.Decode(blockbytes, &blockContainer)
	if err != nil {
		return fmt.Errorf("error decoding blockbytes, %v", err)
	}
	return imp.ImportDecodedBlock(&blockContainer)
}
func (imp *dbImporter) ImportDecodedBlock(blockContainer *types.EncodedBlockCert) (err error) {
	err = imp.db.StartBlock()
	if err != nil {
		return fmt.Errorf("error starting block, %v", err)
	}
	block := blockContainer.Block
	round := uint64(block.Round)
	for intra, stxn := range block.Payset {
		txtype := string(stxn.Txn.Type)
		txtypeenum := TypeEnumMap[txtype]
		assetid := uint64(0)
		switch txtypeenum {
		case 3:
			assetid = uint64(stxn.Txn.ConfigAsset)
		case 4:
			assetid = uint64(stxn.Txn.XferAsset)
		case 5:
			assetid = uint64(stxn.Txn.FreezeAsset)
		}
		if stxn.HasGenesisID {
			stxn.Txn.GenesisID = block.GenesisID
		}
		if stxn.HasGenesisHash {
			stxn.Txn.GenesisHash = block.GenesisHash
		}
		stxnad := stxn.SignedTxnWithAD
		participants := make([][]byte, 0, 10)
		participants = participate(participants, stxn.Txn.Sender[:])
		participants = participate(participants, stxn.Txn.Receiver[:])
		participants = participate(participants, stxn.Txn.CloseRemainderTo[:])
		participants = participate(participants, stxn.Txn.AssetSender[:])
		participants = participate(participants, stxn.Txn.AssetReceiver[:])
		participants = participate(participants, stxn.Txn.AssetCloseTo[:])
		err = imp.db.AddTransaction(round, intra, txtypeenum, assetid, stxnad, participants)
		if err != nil {
			return fmt.Errorf("error importing txn r=%d i=%d, %v", round, intra, err)
		}
	}
	blockHeader := block
	blockHeader.Payset = nil
	blockheaderBytes := msgpack.Encode(blockHeader)
	err = imp.db.CommitBlock(round, block.TimeStamp, block.RewardsLevel, blockheaderBytes)
	if err != nil {
		return fmt.Errorf("error committing block, %v", err)
	}
	return nil
}

func NewDBImporter(db idb.IndexerDb) Importer {
	return &dbImporter{db: db}
}
