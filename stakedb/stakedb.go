// Copyright (c) 2017, Jonathan Chappelow
// See LICENSE for details.

package stakedb

import (
	"encoding/binary"
	"fmt"
	"github.com/HcashOrg/hcd/blockchain/aistake"
	"os"
	"strings"
	"sync"

	"github.com/HcashOrg/hcd/blockchain/stake"
	"github.com/HcashOrg/hcd/chaincfg"
	"github.com/HcashOrg/hcd/chaincfg/chainhash"
	"github.com/HcashOrg/hcd/database"
	"github.com/HcashOrg/hcd/hcutil"
	"github.com/HcashOrg/hcd/wire"
	apitypes "github.com/HcashOrg/hcexplorer/hcdataapi"
	"github.com/HcashOrg/hcexplorer/rpcutils"
	"github.com/HcashOrg/hcexplorer/txhelpers"
	"github.com/HcashOrg/hcrpcclient"
)

// PoolInfoCache contains a map of block hashes to ticket pool info data at that
// block height.
type PoolInfoCache struct {
	sync.RWMutex
	poolInfo map[chainhash.Hash]*apitypes.TicketPoolInfo
}

type AiPoolInfoCache struct {
	sync.RWMutex
	aiPoolInfo map[chainhash.Hash]*apitypes.AiTicketPoolInfo
}

// NewPoolInfoCache constructs a new PoolInfoCache, and is needed to initialize
// the internal map.
func NewPoolInfoCache() *PoolInfoCache {
	return &PoolInfoCache{
		poolInfo: make(map[chainhash.Hash]*apitypes.TicketPoolInfo),
	}
}
func NewAiPoolInfoCache() *AiPoolInfoCache {
	return &AiPoolInfoCache{
		aiPoolInfo: make(map[chainhash.Hash]*apitypes.AiTicketPoolInfo),
	}
}

// Get attempts to fetch the ticket pool info for a given block hash, returning
// a *apitypes.TicketPoolInfo, and a bool indicating if the hash was found in
// the map.
func (c *PoolInfoCache) Get(hash chainhash.Hash) (*apitypes.TicketPoolInfo, bool) {
	c.RLock()
	defer c.RUnlock()
	tpi, ok := c.poolInfo[hash]
	return tpi, ok
}

// Set stores the ticket pool info for the given hash in the pool info cache.
func (c *PoolInfoCache) Set(hash chainhash.Hash, p *apitypes.TicketPoolInfo) {
	c.Lock()
	defer c.Unlock()
	c.poolInfo[hash] = p
}

// StakeDatabase models data for the stake database
type StakeDatabase struct {
	params     *chaincfg.Params
	NodeClient *hcrpcclient.Client
	nodeMtx    sync.RWMutex
	StakeDB    database.DB
	BestNode   *stake.Node
	AiBestNode *aistake.Node
	blkMtx     sync.RWMutex
	blockCache map[int64]*hcutil.Block

	liveTicketMtx   sync.Mutex
	liveTicketCache map[chainhash.Hash]int64
	poolInfo        *PoolInfoCache
	aiPoolInfo      *AiPoolInfoCache

	// added for ai
	aiLiveTicketMtx   sync.Mutex
	aiLiveTicketCache map[chainhash.Hash]int64
}

const (
	// dbType is the database backend type to use
	dbType = "ffldb"
	// DefaultStakeDbName is the default database name
	DefaultStakeDbName = "ffldb_stake"
)

// NewStakeDatabase creates a StakeDatabase instance, opening or creating a new
// ffldb-backed stake database, and loads all live tickets into a cache.
func NewStakeDatabase(client *hcrpcclient.Client, params *chaincfg.Params) (*StakeDatabase, error) {
	sDB := &StakeDatabase{
		params:            params,
		NodeClient:        client,
		blockCache:        make(map[int64]*hcutil.Block),
		liveTicketCache:   make(map[chainhash.Hash]int64),
		aiLiveTicketCache: make(map[chainhash.Hash]int64),

		poolInfo:   NewPoolInfoCache(),
		aiPoolInfo: NewAiPoolInfoCache(),
	}
	if err := sDB.Open(); err != nil {
		return nil, err
	}

	nodeHeight, err := client.GetBlockCount()
	if err != nil {
		log.Errorf("Unable to get best block height: %v", err)
	}

	if int64(sDB.Height()) >= nodeHeight-int64(params.TicketPoolSize)/4 {

		liveTickets, err := sDB.NodeClient.LiveTickets()
		if err != nil {
			return sDB, err
		}
		aiLiveTickets, err := sDB.NodeClient.AiLiveTickets()
		if err != nil {
			return sDB, err
		}
		log.Info("Pre-populating live ticket cache...")
		log.Info("Pre-populating  ai live ticket cache...")

		type promiseGetRawTransaction struct {
			result hcrpcclient.FutureGetRawTransactionResult
			ticket *chainhash.Hash
		}
		promisesGetRawTransaction := make([]promiseGetRawTransaction, 0, len(liveTickets))
		aiPromisesGetRawTransaction := make([]promiseGetRawTransaction, 0, len(aiLiveTickets))

		// Send all the live ticket requests
		for _, hash := range liveTickets {
			promisesGetRawTransaction = append(promisesGetRawTransaction, promiseGetRawTransaction{
				result: sDB.NodeClient.GetRawTransactionAsync(hash),
				ticket: hash,
			})
		}
		// Send all the ai live ticket requests
		for _, hash := range aiLiveTickets {
			aiPromisesGetRawTransaction = append(aiPromisesGetRawTransaction, promiseGetRawTransaction{
				result: sDB.NodeClient.GetRawTransactionAsync(hash),
				ticket: hash,
			})
		}

		// Receive the live ticket tx results
		for _, p := range promisesGetRawTransaction {
			ticketTx, err := p.result.Receive()
			if err != nil {
				log.Error(err)
				continue
			}
			if !ticketTx.Hash().IsEqual(p.ticket) {
				panic(fmt.Sprintf("Failed to receive Tx details for requested ticket hash: %v, %v", p.ticket, ticketTx.Hash()))
			}

			sDB.liveTicketCache[*p.ticket] = ticketTx.MsgTx().TxOut[0].Value

			// txHeight := ticketTx.BlockHeight
			// unconfirmed := (txHeight == 0)
			// immature := (tipHeight-int32(txHeight) < int32(w.ChainParams().TicketMaturity))
		}
		// Receive the live ticket tx results
		for _, p := range aiPromisesGetRawTransaction {
			aiticketTx, err := p.result.Receive()
			if err != nil {
				log.Error(err)
				continue
			}
			if !aiticketTx.Hash().IsEqual(p.ticket) {
				panic(fmt.Sprintf("Failed to receive Tx details for requested ticket hash: %v, %v", p.ticket, aiticketTx.Hash()))
			}

			sDB.aiLiveTicketCache[*p.ticket] = aiticketTx.MsgTx().TxOut[0].Value

			// txHeight := ticketTx.BlockHeight
			// unconfirmed := (txHeight == 0)
			// immature := (tipHeight-int32(txHeight) < int32(w.ChainParams().TicketMaturity))
		}

		// Old synchronous way
		// for _, hash := range liveTickets {
		// 	var txid *hcutil.Tx
		// 	txid, err = sDB.NodeClient.GetRawTransaction(hash)
		// 	if err != nil {
		// 		log.Errorf("Unable to get transaction %v: %v\n", hash, err)
		// 		continue
		// 	}
		// 	// This isn't quite right for pool tickets where the small
		// 	// pool fees are included in vout[0], but it's close.
		// 	sDB.liveTicketCache[*hash] = txid.MsgTx().TxOut[0].Value
		// }
	}

	return sDB, nil
}

// Height gets the block height of the best stake node.  It is thread-safe,
// unlike using db.BestNode.Height(), and checks that the stake database is
// opened first.
func (db *StakeDatabase) Height() uint32 {
	if db == nil || db.BestNode == nil {
		log.Error("Stake database not yet opened")
		return 0
	}
	db.nodeMtx.RLock()
	defer db.nodeMtx.RUnlock()
	return db.BestNode.Height()
}

func (db *StakeDatabase) HeightWithErr() (uint32, error) {
	if db == nil || db.BestNode == nil {
		log.Error("Stake database not yet opened")
		return 0, nil
	}
	db.nodeMtx.RLock()
	defer db.nodeMtx.RUnlock()
	if db.BestNode.Height() != db.AiBestNode.Height() {
		log.Warnf("bestnode height:%d is not equal to aibestNode height:%d",
			db.BestNode.Height(), db.AiBestNode.Height())
		return 0, fmt.Errorf("bestnode height:%d is not equal to aibestNode height:%d",
			db.BestNode.Height(), db.AiBestNode.Height())
	}
	return db.BestNode.Height(), nil
}

// block first tries to find the block at the input height in cache, and if that
// fails it will request it from the node RPC client. Don't use this casually
// since reorganization may redefine a block at a given height.
func (db *StakeDatabase) block(ind int64) (*hcutil.Block, bool) {
	db.blkMtx.RLock()
	block, ok := db.blockCache[ind]
	db.blkMtx.RUnlock()
	//log.Info(ind, block, ok)
	if !ok {
		var err error
		block, _, err = rpcutils.GetBlock(ind, db.NodeClient)
		if err != nil {
			log.Error(err)
			return nil, false
		}
	}
	return block, ok
}

// ForgetBlock deletes the block with the input height from the block cache.
func (db *StakeDatabase) ForgetBlock(ind int64) {
	db.blkMtx.Lock()
	defer db.blkMtx.Unlock()
	delete(db.blockCache, ind)
}

// ConnectBlockHash is a wrapper for ConnectBlock. For the input block hash, it
// gets the block from the node RPC client and calls ConnectBlock.
func (db *StakeDatabase) ConnectBlockHash(hash *chainhash.Hash) (*hcutil.Block, error) {
	msgBlock, err := db.NodeClient.GetBlock(hash)
	if err != nil {
		return nil, err
	}
	block := hcutil.NewBlock(msgBlock)
	return block, db.ConnectBlock(block)
}

// ConnectBlock connects the input block to the tip of the stake DB and updates
// the best stake node. This exported function gets any revoked and spend
// tickets from the input block, and any maturing tickets from the past block in
// which those tickets would be found, and passes them to connectBlock.
func (db *StakeDatabase) ConnectBlock(block *hcutil.Block) error {
	height := block.Height()
	maturingHeight := height - int64(db.params.TicketMaturity)
	aiMaturingHeight := height - int64(db.params.AiTicketMaturity)
	var maturingTickets []chainhash.Hash
	var maturingAiTickets []chainhash.Hash
	if maturingHeight >= 0 {
		maturingBlock, wasCached := db.block(maturingHeight)
		if wasCached {
			db.ForgetBlock(maturingHeight)
		}
		maturingTickets, _ = txhelpers.TicketsInBlock(maturingBlock)

		aiMaturingBlock, wasAiCached := db.block(aiMaturingHeight)
		if wasAiCached {
			db.ForgetBlock(aiMaturingHeight)
		}

		maturingAiTickets, _ = txhelpers.AiTicketsInBlock(aiMaturingBlock)

	}

	db.blkMtx.Lock()
	db.blockCache[block.Height()] = block
	db.blkMtx.Unlock()

	revokedTickets := txhelpers.RevokedTicketsInBlock(block)
	spentTickets := txhelpers.TicketsSpentInBlock(block)

	revotedAiTickets := txhelpers.RevokedAiTicketsInBlock(block)
	spentAiTickets := txhelpers.AiTicketsSpentInBlock(block)

	db.nodeMtx.Lock()
	bestNodeHeight := int64(db.BestNode.Height())
	db.nodeMtx.Unlock()
	if height <= bestNodeHeight {
		return fmt.Errorf("cannot connect block height %d at height %d", height, bestNodeHeight)
	}

	return db.connectBlock(block, spentTickets, spentAiTickets, revokedTickets, revotedAiTickets, maturingTickets, maturingAiTickets)
}

func (db *StakeDatabase) connectBlock(block *hcutil.Block, spent []chainhash.Hash, aiSpent []chainhash.Hash,
	revoked []chainhash.Hash, aiRevoked []chainhash.Hash, maturing []chainhash.Hash, aimaturing []chainhash.Hash) error {
	db.nodeMtx.Lock()

	cleanLiveTicketCache := func() {
		db.liveTicketMtx.Lock()
		for i := range spent {
			delete(db.liveTicketCache, spent[i])
		}
		for i := range revoked {
			delete(db.liveTicketCache, revoked[i])
		}
		db.liveTicketMtx.Unlock()
	}
	defer cleanLiveTicketCache()

	cleanAiLiveTicketCache := func() {
		db.aiLiveTicketMtx.Lock()
		for i := range aiSpent {
			delete(db.aiLiveTicketCache, aiSpent[i])
		}
		for i := range aiRevoked {
			delete(db.aiLiveTicketCache, aiRevoked[i])
		}
		db.aiLiveTicketMtx.Unlock()
	}
	defer cleanAiLiveTicketCache()

	var err error
	db.BestNode, err = db.BestNode.ConnectNode(block.MsgBlock().Header,
		spent, revoked, maturing)
	if err != nil {
		log.Errorf("bestnode connectNode failed:%v", err)
		return err
	}

	db.AiBestNode, err = db.AiBestNode.ConnectNode(block.MsgBlock().Header,
		aiSpent, aiRevoked, aimaturing)
	if err != nil {
		log.Errorf("bestnode connectNode failed:%v", err)
		return err
	}

	if err = db.StakeDB.Update(func(dbTx database.Tx) error {
		return stake.WriteConnectedBestNode(dbTx, db.BestNode, *block.Hash())

	}); err != nil {
		return err
	}

	if err = db.StakeDB.Update(func(dbTx database.Tx) error {
		return aistake.WriteConnectedBestNode(dbTx, db.AiBestNode, *block.Hash())

	}); err != nil {
		return err
	}

	db.nodeMtx.Unlock()

	// Get ticket pool info at current best (just connected in stakedb) block,
	// and store it in the StakeDatabase's PoolInfoCache.
	tpi, _ := db.PoolInfoBest()
	db.poolInfo.Set(*block.Hash(), &tpi)

	return err
}

// DisconnectBlock attempts to disconnect the current best block from the stake
// DB and updates the best stake node.
func (db *StakeDatabase) DisconnectBlock() error {
	db.nodeMtx.Lock()
	defer db.nodeMtx.Unlock()

	return db.disconnectBlock()
}

// disconnectBlock is the non-thread-safe version of DisconnectBlock.
func (db *StakeDatabase) disconnectBlock() error {
	childHeight := db.BestNode.Height()
	aiChildHeight := db.AiBestNode.Height()
	parentBlock, err := db.dbPrevBlock()
	if err != nil {
		return err
	}
	if parentBlock.Height() != int64(childHeight)-1 || parentBlock.Height() != int64(aiChildHeight)-1 {
		panic("BestNode and stake DB or aiStake DB are inconsistent")
	}

	childUndoData := append(stake.UndoTicketDataSlice(nil), db.BestNode.UndoData()...)
	aiChildUndoData := append(aistake.UndoTicketDataSlice(nil), db.AiBestNode.UndoData()...)
	log.Debugf("Disconnecting block %d.", childHeight)

	// previous best node
	var parentStakeNode *stake.Node
	err = db.StakeDB.View(func(dbTx database.Tx) error {
		var errLocal error
		parentStakeNode, errLocal = db.BestNode.DisconnectNode(
			parentBlock.MsgBlock().Header, nil, nil, dbTx)
		return errLocal
	})
	var aiParentStakeNode *aistake.Node
	err = db.StakeDB.View(func(dbtx database.Tx) error {
		var errLocal error
		aiParentStakeNode, errLocal = db.AiBestNode.DisconnectNode(
			parentBlock.MsgBlock().Header, nil, nil, dbtx)
		return errLocal
	})

	if err != nil {
		return err
	}
	db.BestNode = parentStakeNode
	db.AiBestNode = aiParentStakeNode

	return db.StakeDB.Update(func(dbTx database.Tx) error {
		if err := stake.WriteDisconnectedBestNode(dbTx, parentStakeNode,
			*parentBlock.Hash(), childUndoData); err != nil {
			return err
		}
		return aistake.WriteDisconnectedBestNode(dbTx, aiParentStakeNode,
			*parentBlock.Hash(), aiChildUndoData)
	})
}

// DisconnectBlocks disconnects N blocks from the head of the chain.
func (db *StakeDatabase) DisconnectBlocks(count int64) error {
	db.nodeMtx.Lock()
	defer db.nodeMtx.Unlock()

	for i := int64(0); i < count; i++ {
		if err := db.disconnectBlock(); err != nil {
			return err
		}
	}

	return nil
}

// Open attempts to open an existing stake database, and will create a new one
// if one does not exist.
func (db *StakeDatabase) Open() error {
	db.nodeMtx.Lock()
	defer db.nodeMtx.Unlock()

	// Create a new database to store the accepted stake node data into.
	dbName := DefaultStakeDbName
	var err error
	db.StakeDB, err = database.Open(dbType, dbName, db.params.Net)
	if err != nil {
		if strings.Contains(err.Error(), "resource temporarily unavailable") ||
			strings.Contains(err.Error(), "is being used by another process") {
			return fmt.Errorf("Stake DB already opened. hcexplorer running?")
		}
		log.Infof("Unable to open stake DB (%v). Removing and creating new.", err)
		_ = os.RemoveAll(dbName)
		db.StakeDB, err = database.Create(dbType, dbName, db.params.Net)
		if err != nil {
			// do not return nil interface, but interface of nil DB
			return fmt.Errorf("error creating db: %v", err)
		}
	}

	// Load the best block from stake db
	err = db.StakeDB.View(func(dbTx database.Tx) error {
		v := dbTx.Metadata().Get([]byte("stakechainstate"))
		if v == nil {
			return fmt.Errorf("missing stakechainstake key for chain state data")
		}
		var stakeDBHash chainhash.Hash
		copy(stakeDBHash[:], v[:chainhash.HashSize])
		offset := chainhash.HashSize
		stakeDBHeight := binary.LittleEndian.Uint32(v[offset : offset+4])

		aiv := dbTx.Metadata().Get([]byte("aistakechainstate"))
		if aiv == nil {
			return fmt.Errorf("missing aistakechainstate key for chain state data")
		}
		var aistakeDBHash chainhash.Hash
		copy(aistakeDBHash[:], aiv[:chainhash.HashSize])
		aistakeDBHeight := binary.LittleEndian.Uint32(v[offset : offset+4])

		if stakeDBHeight != aistakeDBHeight {
			return fmt.Errorf("stakeDBHeight:%d is not equal to aistakeDBHeight:%d ", stakeDBHeight, aistakeDBHeight)
		}

		var errLocal error
		msgBlock, errLocal := db.NodeClient.GetBlock(&stakeDBHash)
		if errLocal != nil {
			return fmt.Errorf("GetBlock failed (%s): %v", stakeDBHash, errLocal)
		}
		header := msgBlock.Header

		db.BestNode, errLocal = stake.LoadBestNode(dbTx, stakeDBHeight,
			stakeDBHash, header, db.params)
		if errLocal != nil {
			return fmt.Errorf("LoadBestNode failed(%s):%v ", stakeDBHash, errLocal)
		}

		db.AiBestNode, errLocal = aistake.LoadBestNode(dbTx, aistakeDBHeight,
			aistakeDBHash, header, db.params)
		return errLocal
	})
	if err != nil {
		log.Errorf("Error reading from database (%v).  Reinitializing.", err)
		err = db.StakeDB.Update(func(dbTx database.Tx) error {
			var errLocal error
			db.BestNode, errLocal = stake.InitDatabaseState(dbTx, db.params)
			if errLocal != nil {
				log.Errorf("stake initDatabaseState failed:%v", errLocal)
				return errLocal
			}
			db.AiBestNode, errLocal = aistake.InitDatabaseState(dbTx, db.params)
			if errLocal != nil {
				log.Errorf("aistake initDatabaseState failed:%v", errLocal)
			}
			return errLocal
		})
		log.Debug("Created new stake db.")
	} else {
		log.Debug("Opened existing stake db.")
	}

	return err
}

// PoolInfoBest computes ticket pool value using the database and, if needed, the
// node RPC client to fetch ticket values that are not cached. Returned are a
// structure including ticket pool value, size, and average value.
func (db *StakeDatabase) PoolInfoBest() (apitypes.TicketPoolInfo, uint32) {
	db.nodeMtx.RLock()
	poolSize := db.BestNode.PoolSize()
	liveTickets := db.BestNode.LiveTickets()
	height := db.BestNode.Height()

	aiPoolSize := db.AiBestNode.PoolSize()
	aiLiveTickets := db.AiBestNode.LiveTickets()
	db.nodeMtx.RUnlock()

	db.liveTicketMtx.Lock()
	var poolValue int64
	for _, hash := range liveTickets {
		val, ok := db.liveTicketCache[hash]
		if !ok {
			tx, err := db.NodeClient.GetRawTransaction(&hash)
			if err != nil {
				log.Errorf("Unable to get transaction %v: %v\n", hash, err)
				continue
			}
			// This isn't quite right for pool tickets where the small
			// pool fees are included in vout[0], but it's close.
			val = tx.MsgTx().TxOut[0].Value
			db.liveTicketCache[hash] = val
		}
		poolValue += val
	}
	db.liveTicketMtx.Unlock()

	db.aiLiveTicketMtx.Lock()
	var aiPoolValue int64
	for _, hash := range aiLiveTickets {
		val, ok := db.aiLiveTicketCache[hash]
		if !ok {
			tx, err := db.NodeClient.GetRawTransaction(&hash)
			if err != nil {
				log.Errorf("Unable to get transaction %v: %v\n", hash, err)
				continue
			}
			// This isn't quite right for pool tickets where the small
			// pool fees are included in vout[0], but it's close.
			val = tx.MsgTx().TxOut[0].Value
			db.aiLiveTicketCache[hash] = val
		}
		aiPoolValue += val
	}
	db.aiLiveTicketMtx.Unlock()

	// header, _ := db.DBTipBlockHeader()
	// if int(header.PoolSize) != len(liveTickets) {
	// 	log.Infof("Header at %d, DB at %d.", header.Height, db.BestNode.Height())
	// 	log.Warnf("Inconsistent pool sizes: %d, %d", header.PoolSize, len(liveTickets))
	// }

	poolCoin := hcutil.Amount(poolValue).ToCoin()
	aiPoolCoin := hcutil.Amount(aiPoolValue).ToCoin()
	valAvg := 0.0
	aiValAvg := 0.0
	if len(liveTickets) > 0 {
		valAvg = poolCoin / float64(poolSize)
	}
	if len(aiLiveTickets) > 0 {
		aiValAvg = aiPoolCoin / float64((aiPoolSize))
	}

	return apitypes.TicketPoolInfo{
		Size:   uint32(poolSize),
		Value:  poolCoin,
		ValAvg: valAvg,

		AiSize:   uint32(aiPoolSize),
		AiValue:  aiPoolCoin,
		AiValAvg: aiValAvg,
	}, height
}

// PoolInfo attempts to fetch the ticket pool info for the specified block hash
// from an internal pool info cache. If it is not found, you should attempt to
// use PoolInfoBest if the target block is at the tip of the chain.
func (db *StakeDatabase) PoolInfo(hash chainhash.Hash) (*apitypes.TicketPoolInfo, bool) {
	return db.poolInfo.Get(hash)
}

// PoolSize returns the ticket pool size in the best node of the stake database
func (db *StakeDatabase) PoolSize() int {
	return db.BestNode.PoolSize()
}

// DBState queries the stake database for the best block height and hash.
func (db *StakeDatabase) DBState() (uint32, *chainhash.Hash, error) {
	db.nodeMtx.RLock()
	defer db.nodeMtx.RUnlock()

	return db.dbState()
}

func (db *StakeDatabase) dbState() (uint32, *chainhash.Hash, error) {
	var stakeDBHeight uint32
	var stakeDBHash chainhash.Hash
	err := db.StakeDB.View(func(dbTx database.Tx) error {
		v := dbTx.Metadata().Get([]byte("stakechainstate"))
		if v == nil {
			return fmt.Errorf("missing key for chain state data")
		}

		copy(stakeDBHash[:], v[:chainhash.HashSize])
		offset := chainhash.HashSize
		stakeDBHeight = binary.LittleEndian.Uint32(v[offset : offset+4])

		return nil
	})
	return stakeDBHeight, &stakeDBHash, err
}
func (db *StakeDatabase) aiDbState() (uint32, *chainhash.Hash, error) {
	var aistakeDBHeight uint32
	var aistakeDBHash chainhash.Hash
	err := db.StakeDB.View(func(dbTx database.Tx) error {
		v := dbTx.Metadata().Get([]byte("aistakechainstate"))
		if v == nil {
			return fmt.Errorf("missing key for chain ai state data")
		}

		copy(aistakeDBHash[:], v[:chainhash.HashSize])
		offset := chainhash.HashSize
		aistakeDBHeight = binary.LittleEndian.Uint32(v[offset : offset+4])

		return nil
	})
	return aistakeDBHeight, &aistakeDBHash, err
}

// DBTipBlockHeader gets the block header for the current best block in the
// stake database. It used DBState to get the best block hash, and the node RPC
// client to get the header.
func (db *StakeDatabase) DBTipBlockHeader() (*wire.BlockHeader, error) {
	_, hash, err := db.DBState()
	if err != nil {
		return nil, err
	}

	return db.NodeClient.GetBlockHeader(hash)
}

// DBPrevBlockHeader gets the block header for the previous best block in the
// stake database. It used DBState to get the best block hash, and the node RPC
// client to get the header.
func (db *StakeDatabase) DBPrevBlockHeader() (*wire.BlockHeader, error) {
	_, hash, err := db.DBState()
	if err != nil {
		return nil, err
	}

	parentHeader, err := db.NodeClient.GetBlockHeader(hash)
	if err != nil {
		return nil, err
	}

	return db.NodeClient.GetBlockHeader(&parentHeader.PrevBlock)
}

// DBTipBlock gets the hcutil.Block for the current best block in the stake
// database. It used DBState to get the best block hash, and the node RPC client
// to get the block itself.
func (db *StakeDatabase) DBTipBlock() (*hcutil.Block, error) {
	_, hash, err := db.DBState()
	if err != nil {
		return nil, err
	}

	return db.getBlock(hash)
}

// DBPrevBlock gets the hcutil.Block for the previous best block in the stake
// database. It used DBState to get the best block hash, and the node RPC client
// to get the block itself.
func (db *StakeDatabase) DBPrevBlock() (*hcutil.Block, error) {
	_, hash, err := db.DBState()
	if err != nil {
		return nil, err
	}

	parentHeader, err := db.NodeClient.GetBlockHeader(hash)
	if err != nil {
		return nil, err
	}

	return db.getBlock(&parentHeader.PrevBlock)
}

// dbPrevBlock is the non-thread-safe version of DBPrevBlock.
func (db *StakeDatabase) dbPrevBlock() (*hcutil.Block, error) {
	_, hash, err := db.dbState()
	if err != nil {
		return nil, err
	}

	parentHeader, err := db.NodeClient.GetBlockHeader(hash)
	if err != nil {
		return nil, err
	}

	return db.getBlock(&parentHeader.PrevBlock)
}

func (db *StakeDatabase) getBlock(hash *chainhash.Hash) (*hcutil.Block, error) {
	msgBlock, err := db.NodeClient.GetBlock(hash)
	if err == nil {
		return hcutil.NewBlock(msgBlock), nil
	}
	return nil, err
}
