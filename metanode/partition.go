// Copyright 2018 The Chubao Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License.

package metanode

import (
	"bytes"
	"container/list"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chubaofs/chubaofs/cmd/common"
	"github.com/chubaofs/chubaofs/proto"
	"github.com/chubaofs/chubaofs/raftstore"
	"github.com/chubaofs/chubaofs/util"
	"github.com/chubaofs/chubaofs/util/errors"
	"github.com/chubaofs/chubaofs/util/log"
	raftproto "github.com/tiglabs/raft/proto"
)

var (
	ErrIllegalHeartbeatAddress = errors.New("illegal heartbeat address")
	ErrIllegalReplicateAddress = errors.New("illegal replicate address")
)

// Errors
var (
	ErrInodeIDOutOfRange = errors.New("inode ID out of range")
)

type sortedPeers []proto.Peer

func (sp sortedPeers) Len() int {
	return len(sp)
}
func (sp sortedPeers) Less(i, j int) bool {
	return sp[i].ID < sp[j].ID
}

func (sp sortedPeers) Swap(i, j int) {
	sp[i], sp[j] = sp[j], sp[i]
}

// MetaPartitionConfig is used to create a meta partition.
type MetaPartitionConfig struct {
	// Identity for raftStore group. RaftStore nodes in the same raftStore group must have the same groupID.
	PartitionId uint64              `json:"partition_id"`
	VolName     string              `json:"vol_name"`
	Start       uint64              `json:"start"` // Minimal Inode ID of this range. (Required during initialization)
	End         uint64              `json:"end"`   // Maximal Inode ID of this range. (Required during initialization)
	Peers       []proto.Peer        `json:"peers"` // Peers information of the raftStore
	StoreType   proto.StoreType     `json:"store_type"`
	Learners    []proto.Learner     `json:"learners"` // Learners information of the raftStore
	Cursor      uint64              `json:"-"`        // Cursor ID of the inode that have been assigned
	NodeId      uint64              `json:"-"`
	RootDir     string              `json:"-"`
	RocksDir    string              `json:"-"`
	BeforeStop  func()              `json:"-"`
	AfterStop   func()              `json:"-"`
	RaftStore   raftstore.RaftStore `json:"-"`
	ConnPool    *util.ConnectPool   `json:"-"`
}

func (c *MetaPartitionConfig) checkMeta() (err error) {
	if c.PartitionId <= 0 {
		err = errors.NewErrorf("[checkMeta]: partition id at least 1, "+
			"now partition id is: %d", c.PartitionId)
		return
	}
	if c.Start < 0 {
		err = errors.NewErrorf("[checkMeta]: start at least 0")
		return
	}
	if c.End <= c.Start {
		err = errors.NewErrorf("[checkMeta]: end=%v, "+
			"start=%v; end <= start", c.End, c.Start)
		return
	}
	if len(c.Peers) <= 0 {
		err = errors.NewErrorf("[checkMeta]: must have peers, now peers is 0")
		return
	}
	return
}

func (c *MetaPartitionConfig) sortPeers() {
	sp := sortedPeers(c.Peers)
	sort.Sort(sp)
}

// metaPartition manages the range of the inode IDs.
// When a new inode is requested, it allocates a new inode id for this inode if possible.
// States:
//  +-----+             +-------+
//  | New | → Restore → | Ready |
//  +-----+             +-------+
type MetaPartition struct {
	config                 *MetaPartitionConfig
	size                   uint64 // For partition all file size
	applyID                uint64 // Inode/Dentry max applyID, this index will be update after restoring from the dumped data.
	dentryTree             DentryTree
	inodeTree              InodeTree     // btree for inodes
	extendTree             ExtendTree    // btree for inode extend (XAttr) management
	multipartTree          MultipartTree // collection for multipart management
	raftPartition          raftstore.Partition
	stopC                  chan bool
	storeChan              chan *storeMsg
	state                  uint32
	delInodeFp             *os.File
	freeList               *freeList // free inode list
	extDelCh               chan []proto.ExtentKey
	extReset               chan struct{}
	vol                    *Vol
	manager                *metadataManager
	persistedApplyID       uint64
	isLoadingMetaPartition bool
	inodeLock              sync.Mutex
	inodeIDQueue           list.List
}

func (mp *MetaPartition) ForceSetMetaPartitionToLoadding() {
	mp.isLoadingMetaPartition = true
}

func (mp *MetaPartition) ForceSetMetaPartitionToFininshLoad() {
	mp.isLoadingMetaPartition = false
}

// Start starts a meta partition.
func (mp *MetaPartition) Start() (err error) {
	if atomic.CompareAndSwapUint32(&mp.state, common.StateStandby, common.StateStart) {
		defer func() {
			var newState uint32
			if err != nil {
				newState = common.StateStandby
			} else {
				newState = common.StateRunning
			}
			atomic.StoreUint32(&mp.state, newState)
		}()

		if err = mp.onStart(); err != nil {
			err = errors.NewErrorf("[Start]->%s", err.Error())
			return
		}
	}
	return
}

// Stop stops a meta partition.
func (mp *MetaPartition) Stop() {
	if atomic.CompareAndSwapUint32(&mp.state, common.StateRunning, common.StateShutdown) {
		defer atomic.StoreUint32(&mp.state, common.StateStopped)
		if mp.config.BeforeStop != nil {
			mp.config.BeforeStop()
		}
		mp.onStop()
		if mp.config.AfterStop != nil {
			mp.config.AfterStop()
			log.LogDebugf("[AfterStop]: partition id=%d execute ok.",
				mp.config.PartitionId)
		}
	}
}

func (mp *MetaPartition) onStart() (err error) {
	defer func() {
		if err == nil {
			return
		}
		mp.onStop()
	}()
	if err = mp.load(); err != nil {
		err = errors.NewErrorf("[onStart]:load partition id=%d: %s",
			mp.config.PartitionId, err.Error())
		return
	}
	if mp.config.StoreType == proto.MetaTypeRocks {
		go mp.startScheduleByRocksDB()
	} else {
		mp.startSchedule(mp.applyID)
	}

	if err = mp.startFreeList(); err != nil {
		err = errors.NewErrorf("[onStart] start free list id=%d: %s",
			mp.config.PartitionId, err.Error())
		return
	}
	if err = mp.startRaft(); err != nil {
		err = errors.NewErrorf("[onStart]start raft id=%d: %s",
			mp.config.PartitionId, err.Error())
		return
	}
	return
}

func (mp *MetaPartition) onStop() {
	mp.stopRaft()
	mp.stop()
	if mp.delInodeFp != nil {
		mp.delInodeFp.Sync()
		mp.delInodeFp.Close()
	}
}

func (mp *MetaPartition) startRaft() (err error) {
	var (
		heartbeatPort int
		replicaPort   int
		peers         []raftstore.PeerAddress
		learners      []raftproto.Learner
	)
	if heartbeatPort, replicaPort, err = mp.getRaftPort(); err != nil {
		return
	}
	for _, peer := range mp.config.Peers {
		addr := strings.Split(peer.Addr, ":")[0]
		rp := raftstore.PeerAddress{
			Peer: raftproto.Peer{
				ID: peer.ID,
			},
			Address:       addr,
			HeartbeatPort: heartbeatPort,
			ReplicaPort:   replicaPort,
		}
		peers = append(peers, rp)
	}
	log.LogDebugf("start partition id=%d raft peers: %s", mp.config.PartitionId, peers)

	for _, learner := range mp.config.Learners {
		rl := raftproto.Learner{ID: learner.ID, PromConfig: &raftproto.PromoteConfig{AutoPromote: learner.PmConfig.AutoProm, PromThreshold: learner.PmConfig.PromThreshold}}
		learners = append(learners, rl)
	}
	pc := &raftstore.PartitionConfig{
		ID:       mp.config.PartitionId,
		Applied:  mp.applyID,
		Peers:    peers,
		Learners: learners,
		SM:       mp,
	}
	mp.raftPartition, err = mp.config.RaftStore.CreatePartition(pc)
	if err == nil {
		mp.ForceSetMetaPartitionToFininshLoad()
	}
	return
}

func (mp *MetaPartition) stopRaft() {
	if mp.raftPartition != nil {
		// TODO Unhandled errors
		//mp.raftPartition.Stop()
	}
	return
}

func (mp *MetaPartition) getRaftPort() (heartbeat, replica int, err error) {
	raftConfig := mp.config.RaftStore.RaftConfig()
	heartbeatAddrSplits := strings.Split(raftConfig.HeartbeatAddr, ":")
	replicaAddrSplits := strings.Split(raftConfig.ReplicateAddr, ":")
	if len(heartbeatAddrSplits) != 2 {
		err = ErrIllegalHeartbeatAddress
		return
	}
	if len(replicaAddrSplits) != 2 {
		err = ErrIllegalReplicateAddress
		return
	}
	heartbeat, err = strconv.Atoi(heartbeatAddrSplits[1])
	if err != nil {
		return
	}
	replica, err = strconv.Atoi(replicaAddrSplits[1])
	if err != nil {
		return
	}
	return
}

// NewMetaPartition creates a new meta partition with the specified configuration.
func NewMetaPartition(conf *MetaPartitionConfig, manager *metadataManager) (*MetaPartition, error) {
	var (
		inodeTree     InodeTree
		dentryTree    DentryTree
		extendTree    ExtendTree
		multipartTree MultipartTree
	)

	switch conf.StoreType {
	case proto.MetaTypeMemory:
		inodeTree = &InodeBTree{NewBtree()}
		dentryTree = &DentryBTree{NewBtree()}
		extendTree = &ExtendBTree{NewBtree()}
		multipartTree = &MultipartBTree{NewBtree()}
	case proto.MetaTypeRocks:
		tree, err := DefaultRocksTree(conf.RocksDir)
		if err != nil {
			log.LogErrorf("[NewMetaPartition] default rocks tree dir: %v, id: %v error %v ", conf.RootDir, conf.PartitionId, err)
			return nil, err
		}
		inodeTree, err = NewInodeRocks(tree)
		if err != nil {
			return nil, err
		}
		dentryTree, err = NewDentryRocks(tree)
		if err != nil {
			return nil, err
		}
		extendTree, err = NewExtendRocks(tree)
		if err != nil {
			return nil, err
		}
		multipartTree, err = NewMultipartRocks(tree)
		if err != nil {
			return nil, err
		}
		log.LogInfof("partition:[%d] inode:[%d] dentry:[%d] extend:[%d] multipart:[%d]", conf.PartitionId, inodeTree.Count(), dentryTree.Count(), extendTree.Count(), multipartTree.Count())
	default:
		return nil, fmt.Errorf("unsupport store type for %v", conf.StoreType)
	}

	applyID, err := inodeTree.GetApplyID()
	if err != nil {
		log.LogErrorf("[NewMetaPartition] read applyID has err:[%s] ", err.Error())
		return nil, err
	} else {
		log.LogInfof("load partition tree has ok : appply id is:[%d]", applyID)
	}

	mp := &MetaPartition{
		config:           conf,
		inodeTree:        inodeTree,
		dentryTree:       dentryTree,
		extendTree:       extendTree,
		multipartTree:    multipartTree,
		stopC:            make(chan bool),
		storeChan:        make(chan *storeMsg, 5),
		freeList:         newFreeList(),
		extDelCh:         make(chan []proto.ExtentKey, 10000),
		extReset:         make(chan struct{}),
		vol:              NewVol(),
		manager:          manager,
		applyID:          applyID,
		persistedApplyID: applyID,
	}
	return mp, nil
}

// IsLeader returns the raft leader address and if the current meta partition is the leader.
func (mp *MetaPartition) IsLeader() (leaderAddr string, ok bool) {
	if mp.raftPartition == nil {
		return
	}
	leaderID, _ := mp.raftPartition.LeaderTerm()
	if leaderID == 0 {
		return
	}
	ok = leaderID == mp.config.NodeId
	for _, peer := range mp.config.Peers {
		if leaderID == peer.ID {
			leaderAddr = peer.Addr
			return
		}
	}
	return
}

func (mp *MetaPartition) IsLearner() bool {
	for _, learner := range mp.config.Learners {
		if mp.config.NodeId == learner.ID {
			return true
		}
	}
	return false
}

func (mp *MetaPartition) GetPeers() (peers []string) {
	peers = make([]string, 0)
	for _, peer := range mp.config.Peers {
		if mp.config.NodeId == peer.ID {
			continue
		}
		peers = append(peers, peer.Addr)
	}
	return
}

// GetCursor returns the cursor stored in the config.
func (mp *MetaPartition) GetCursor() uint64 {
	return atomic.LoadUint64(&mp.config.Cursor)
}

// PersistMetadata is the wrapper of persistMetadata.
func (mp *MetaPartition) PersistMetadata() (err error) {
	mp.config.sortPeers()
	err = mp.persistMetadata()
	return
}

func (mp *MetaPartition) GetDentryTree() DentryTree {
	return mp.dentryTree
}

func (mp *MetaPartition) GetInodeTree() InodeTree {
	return mp.inodeTree
}

func (mp *MetaPartition) GetMultipartTree() MultipartTree {
	return mp.multipartTree
}

func (mp *MetaPartition) GetExtendTree() ExtendTree {
	return mp.extendTree
}

func (mp *MetaPartition) LoadSnapshot(snapshotPath string) (err error) {
	if err = loadMetadata(mp.config); err != nil {
		return
	}

	//it means rocksdb and not init so skip load snapshot
	if mp.config.StoreType == proto.MetaTypeRocks && mp.inodeTree.Count() > 0 {
		if mp.applyID, err = mp.inodeTree.GetApplyID(); err != nil {
			return err
		}
		if maxID, err := mp.inodeTree.GetMaxInode(); err != nil {
			return err
		} else {
			mp.config.Cursor = maxID
		}
		return err
	}

	if err = mp.loadInode(snapshotPath); err != nil {
		return
	}
	if err = mp.loadDentry(snapshotPath); err != nil {
		return
	}
	if err = mp.loadExtend(snapshotPath); err != nil {
		return
	}
	if err = mp.loadMultipart(snapshotPath); err != nil {
		return
	}
	err = mp.loadApplyID(snapshotPath)
	return
}

func (mp *MetaPartition) load() (err error) {
	if err = loadMetadata(mp.config); err != nil {
		return
	}
	return mp.LoadSnapshot(path.Join(mp.config.RootDir, snapshotDir))
}

func (mp *MetaPartition) store(sm *storeMsg) (err error) {

	if mp.config.StoreType == proto.MetaTypeRocks {
		defer sm.snapshot.Close()
		return mp.inodeTree.Flush()
	}

	tmpDir := path.Join(mp.config.RootDir, snapshotDirTmp)
	if _, err = os.Stat(tmpDir); err == nil {
		// TODO Unhandled errors
		log.LogIfNotNil(os.RemoveAll(tmpDir))
	}
	err = nil
	if err = os.MkdirAll(tmpDir, 0775); err != nil {
		return
	}

	defer func() {
		if err != nil {
			// TODO Unhandled errors
			log.LogIfNotNil(os.RemoveAll(tmpDir))
		}
	}()
	var crcBuffer = bytes.NewBuffer(make([]byte, 0, 16))
	var storeFuncs = []func(dir string, sm *storeMsg) (uint32, error){
		mp.storeInode,
		mp.storeDentry,
		mp.storeExtend,
		mp.storeMultipart,
	}
	for _, storeFunc := range storeFuncs {
		var crc uint32
		if crc, err = storeFunc(tmpDir, sm); err != nil {
			return
		}
		if crcBuffer.Len() != 0 {
			crcBuffer.WriteString(" ")
		}
		crcBuffer.WriteString(fmt.Sprintf("%d", crc))
	}
	if err = mp.storeApplyID(tmpDir, sm); err != nil {
		return
	}
	// write crc to file
	if err = ioutil.WriteFile(path.Join(tmpDir, SnapshotSign), crcBuffer.Bytes(), 0775); err != nil {
		return
	}
	snapshotDir := path.Join(mp.config.RootDir, snapshotDir)
	// check snapshot backup
	backupDir := path.Join(mp.config.RootDir, snapshotBackup)
	if _, err = os.Stat(backupDir); err == nil {
		if err = os.RemoveAll(backupDir); err != nil {
			return
		}
	}
	err = nil

	// rename snapshot
	if _, err = os.Stat(snapshotDir); err == nil {
		if err = os.Rename(snapshotDir, backupDir); err != nil {
			return
		}
	}
	err = nil

	if err = os.Rename(tmpDir, snapshotDir); err != nil {
		_ = os.Rename(backupDir, snapshotDir)
		return
	}
	err = os.RemoveAll(backupDir)
	return
}

// UpdatePeers updates the peers.
func (mp *MetaPartition) UpdatePeers(peers []proto.Peer) {
	mp.config.Peers = peers
}

// DeleteRaft deletes the raft partition.
func (mp *MetaPartition) DeleteRaft() (err error) {
	err = mp.raftPartition.Delete()
	return
}

// ExpiredRaft deletes the raft partition.
func (mp *MetaPartition) ExpiredRaft() (err error) {
	err = mp.raftPartition.Expired()
	return
}

// Return a new inode ID and update the offset.
func (mp *MetaPartition) nextInodeID() (inodeId uint64, err error) {
	for {
		cur := atomic.LoadUint64(&mp.config.Cursor)
		end := mp.config.End
		if cur >= end {
			return 0, ErrInodeIDOutOfRange
		}
		newId := cur + 1
		if atomic.CompareAndSwapUint64(&mp.config.Cursor, cur, newId) {
			return newId, nil
		}
	}
}

// ChangeMember changes the raft member with the specified one.
func (mp *MetaPartition) ChangeMember(changeType raftproto.ConfChangeType, peer raftproto.Peer, context []byte) (resp interface{}, err error) {
	resp, err = mp.raftPartition.ChangeMember(changeType, peer, context)
	return
}

// GetBaseConfig returns the configuration stored in the meta partition. TODO remove? no usage?
func (mp *MetaPartition) GetBaseConfig() MetaPartitionConfig {
	return *mp.config
}

// UpdatePartition updates the meta partition. TODO remove? no usage?
func (mp *MetaPartition) UpdatePartition(req *UpdatePartitionReq,
	resp *UpdatePartitionResp) (err error) {
	reqData, err := json.Marshal(req)
	if err != nil {
		resp.Status = proto.TaskFailed
		resp.Result = err.Error()
		return
	}
	r, err := mp.submit(opFSMUpdatePartition, reqData)
	if err != nil {
		resp.Status = proto.TaskFailed
		resp.Result = err.Error()
		return
	}
	if status := r.(uint8); status != proto.OpOk {
		resp.Status = proto.TaskFailed
		p := &Packet{}
		p.ResultCode = status
		err = errors.NewErrorf("[UpdatePartition]: %s", p.GetResultMsg())
		resp.Result = p.GetResultMsg()
	}
	resp.Status = proto.TaskSucceeds
	return
}

func (mp *MetaPartition) DecommissionPartition(req []byte) (err error) {
	_, err = mp.submit(opFSMDecommissionPartition, req)
	return
}

func (mp *MetaPartition) IsExistPeer(peer proto.Peer) bool {
	for _, hasExsitPeer := range mp.config.Peers {
		if hasExsitPeer.Addr == peer.Addr && hasExsitPeer.ID == peer.ID {
			return true
		}
	}
	return false
}

func (mp *MetaPartition) IsExistLearner(learner proto.Learner) bool {
	var existPeer bool
	for _, hasExistPeer := range mp.config.Peers {
		if hasExistPeer.Addr == learner.Addr && hasExistPeer.ID == learner.ID {
			existPeer = true
		}
	}
	var existLearner bool
	for _, hasExistLearner := range mp.config.Learners {
		if hasExistLearner.Addr == learner.Addr && hasExistLearner.ID == learner.ID {
			existLearner = true
		}
	}
	return existPeer && existLearner
}

func (mp *MetaPartition) TryToLeader(groupID uint64) error {
	return mp.raftPartition.TryToLeader(groupID)
}

// ResponseLoadMetaPartition loads the snapshot signature. TODO remove? no usage?
func (mp *MetaPartition) ResponseLoadMetaPartition(p *Packet) (err error) {
	resp := &proto.MetaPartitionLoadResponse{
		PartitionID: mp.config.PartitionId,
		DoCompare:   true,
	}
	resp.MaxInode = mp.GetCursor()
	resp.InodeCount = uint64(mp.inodeTree.Count())
	resp.DentryCount = uint64(mp.dentryTree.Count())
	resp.ApplyID = mp.applyID
	resp.StoreType = mp.config.StoreType

	data, err := json.Marshal(resp)
	if err != nil {
		err = errors.Trace(err, "[ResponseLoadMetaPartition] marshal")
		return
	}
	p.PacketOkWithBody(data)
	return
}

// MarshalJSON is the wrapper of json.Marshal.
func (mp *MetaPartition) MarshalJSON() ([]byte, error) {
	return json.Marshal(mp.config)
}

// TODO remove? no usage?
// Reset resets the meta partition.
func (mp *MetaPartition) Reset() (err error) {
	mp.inodeTree.Release()
	mp.dentryTree.Release()
	mp.extendTree.Release()
	mp.multipartTree.Release()

	mp.config.Cursor = 0

	mp.applyID = 0

	// remove files
	filenames := []string{applyIDFile, dentryFile, inodeFile, extendFile, multipartFile}
	for _, filename := range filenames {
		filepath := path.Join(mp.config.RootDir, filename)
		if err = os.Remove(filepath); err != nil {
			return
		}
	}

	return
}

func (mp *MetaPartition) Expired() (err error) {
	mp.stop()
	if mp.delInodeFp != nil {
		// TODO Unhandled errors
		mp.delInodeFp.Sync()
		mp.delInodeFp.Close()
	}

	mp.config.Cursor = 0
	mp.applyID = 0

	currentPath := path.Clean(mp.config.RootDir)

	var newPath = path.Join(path.Dir(currentPath),
		ExpiredPartitionPrefix+path.Base(currentPath)+"_"+strconv.FormatInt(time.Now().Unix(), 10))

	if err := os.Rename(currentPath, newPath); err != nil {
		log.LogErrorf("ExpiredPartition: mark expired partition fail: partitionID(%v) path(%v) newPath(%v) err(%v)", mp.config.PartitionId, currentPath, newPath, err)
		return err
	}
	log.LogInfof("ExpiredPartition: mark expired partition: partitionID(%v) path(%v) newPath(%v)",
		mp.config.PartitionId, currentPath, newPath)
	return nil
}

//
func (mp *MetaPartition) canRemoveSelf() (canRemove bool, err error) {
	var partition *proto.MetaPartitionInfo
	if partition, err = masterClient.ClientAPI().GetMetaPartition(mp.config.PartitionId); err != nil {
		log.LogErrorf("action[canRemoveSelf] err[%v]", err)
		return
	}
	canRemove = false
	var existInPeers bool
	for _, peer := range partition.Peers {
		if mp.config.NodeId == peer.ID {
			existInPeers = true
		}
	}
	if !existInPeers {
		canRemove = true
		return
	}
	if mp.config.NodeId == partition.OfflinePeerID {
		canRemove = true
		return
	}
	return
}
