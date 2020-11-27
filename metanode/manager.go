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
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	_ "net/http/pprof"
	"os"
	"path"
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
	"github.com/chubaofs/chubaofs/util/exporter"
	"github.com/chubaofs/chubaofs/util/log"
)

const partitionPrefix = "partition_"
const ExpiredPartitionPrefix = "expired_"

func partitionPrefixPath(dir string, partitionId string) string {
	return path.Join(dir, partitionPrefix+partitionId)
}

type metadataManager struct {
	nodeId             uint64
	zoneName           string
	rootDir            string
	rocksDirs          []string
	raftStore          raftstore.RaftStore
	connPool           *util.ConnectPool
	state              uint32
	mu                 sync.RWMutex
	partitions         map[uint64]*MetaPartition // Key: metaRangeId, Val: metaPartition
	metaNode           *MetaNode
	flDeleteBatchCount atomic.Value
}

// HandleMetadataOperation handles the metadata operations.
func (m *metadataManager) HandleMetadataOperation(conn net.Conn, p *Packet,
	remoteAddr string) (err error) {
	metric := exporter.NewTPCnt(p.GetOpMsg())
	defer metric.Set(err)

	switch p.Opcode {
	case proto.OpMetaCreateInode:
		err = m.opCreateInode(conn, p, remoteAddr)
	case proto.OpMetaLinkInode:
		err = m.opMetaLinkInode(conn, p, remoteAddr)
	case proto.OpMetaFreeInodesOnRaftFollower:
		err = m.opFreeInodeOnRaftFollower(conn, p, remoteAddr)
	case proto.OpMetaUnlinkInode:
		err = m.opMetaUnlinkInode(conn, p, remoteAddr)
	case proto.OpMetaBatchUnlinkInode:
		err = m.opMetaBatchUnlinkInode(conn, p, remoteAddr)
	case proto.OpMetaInodeGet:
		err = m.opMetaInodeGet(conn, p, remoteAddr)
	case proto.OpMetaEvictInode:
		err = m.opMetaEvictInode(conn, p, remoteAddr)
	case proto.OpMetaBatchEvictInode:
		err = m.opBatchMetaEvictInode(conn, p, remoteAddr)
	case proto.OpMetaSetattr:
		err = m.opSetAttr(conn, p, remoteAddr)
	case proto.OpMetaCreateDentry:
		err = m.opCreateDentry(conn, p, remoteAddr)
	case proto.OpMetaDeleteDentry:
		err = m.opDeleteDentry(conn, p, remoteAddr)
	case proto.OpMetaBatchDeleteDentry:
		err = m.opBatchDeleteDentry(conn, p, remoteAddr)
	case proto.OpMetaUpdateDentry:
		err = m.opUpdateDentry(conn, p, remoteAddr)
	case proto.OpMetaReadDir:
		err = m.opReadDir(conn, p, remoteAddr)
	case proto.OpCreateMetaPartition:
		err = m.opCreateMetaPartition(conn, p, remoteAddr)
	case proto.OpMetaNodeHeartbeat:
		err = m.opMasterHeartbeat(conn, p, remoteAddr)
	case proto.OpMetaExtentsAdd:
		err = m.opMetaExtentsAdd(conn, p, remoteAddr)
	case proto.OpMetaExtentsList:
		err = m.opMetaExtentsList(conn, p, remoteAddr)
	case proto.OpMetaExtentsDel:
		err = m.opMetaExtentsDel(conn, p, remoteAddr)
	case proto.OpMetaTruncate:
		err = m.opMetaExtentsTruncate(conn, p, remoteAddr)
	case proto.OpMetaLookup:
		err = m.opMetaLookup(conn, p, remoteAddr)
	case proto.OpDeleteMetaPartition:
		err = m.opExpiredMetaPartition(conn, p, remoteAddr)
	case proto.OpUpdateMetaPartition:
		err = m.opUpdateMetaPartition(conn, p, remoteAddr)
	case proto.OpLoadMetaPartition:
		err = m.opLoadMetaPartition(conn, p, remoteAddr)
	case proto.OpDecommissionMetaPartition:
		err = m.opDecommissionMetaPartition(conn, p, remoteAddr)
	case proto.OpAddMetaPartitionRaftMember:
		err = m.opAddMetaPartitionRaftMember(conn, p, remoteAddr)
	case proto.OpRemoveMetaPartitionRaftMember:
		err = m.opRemoveMetaPartitionRaftMember(conn, p, remoteAddr)
	case proto.OpAddMetaPartitionRaftLearner:
		err = m.opAddMetaPartitionRaftLearner(conn, p, remoteAddr)
	case proto.OpPromoteMetaPartitionRaftLearner:
		err = m.opPromoteMetaPartitionRaftLearner(conn, p, remoteAddr)
	case proto.OpMetaPartitionTryToLeader:
		err = m.opMetaPartitionTryToLeader(conn, p, remoteAddr)
	case proto.OpMetaBatchInodeGet:
		err = m.opMetaBatchInodeGet(conn, p, remoteAddr)
	case proto.OpMetaDeleteInode:
		err = m.opMetaDeleteInode(conn, p, remoteAddr)
	case proto.OpMetaCursorReset:
		err = m.opMetaCursorReset(conn, p, remoteAddr)
	case proto.OpMetaBatchDeleteInode:
		err = m.opMetaBatchDeleteInode(conn, p, remoteAddr)
	case proto.OpMetaBatchExtentsAdd:
		err = m.opMetaBatchExtentsAdd(conn, p, remoteAddr)
	// operations for extend attributes
	case proto.OpMetaSetXAttr:
		err = m.opMetaSetXAttr(conn, p, remoteAddr)
	case proto.OpMetaGetXAttr:
		err = m.opMetaGetXAttr(conn, p, remoteAddr)
	case proto.OpMetaBatchGetXAttr:
		err = m.opMetaBatchGetXAttr(conn, p, remoteAddr)
	case proto.OpMetaRemoveXAttr:
		err = m.opMetaRemoveXAttr(conn, p, remoteAddr)
	case proto.OpMetaListXAttr:
		err = m.opMetaListXAttr(conn, p, remoteAddr)
	// operations for multipart session
	case proto.OpCreateMultipart:
		err = m.opCreateMultipart(conn, p, remoteAddr)
	case proto.OpListMultiparts:
		err = m.opListMultipart(conn, p, remoteAddr)
	case proto.OpRemoveMultipart:
		err = m.opRemoveMultipart(conn, p, remoteAddr)
	case proto.OpAddMultipartPart:
		err = m.opAppendMultipart(conn, p, remoteAddr)
	case proto.OpGetMultipart:
		err = m.opGetMultipart(conn, p, remoteAddr)
	default:
		err = fmt.Errorf("%s unknown Opcode: %d, reqId: %d", remoteAddr,
			p.Opcode, p.GetReqID())
	}
	if err != nil {
		err = errors.NewErrorf("%s [%s] req: %d - %s", remoteAddr, p.GetOpMsg(),
			p.GetReqID(), err.Error())
	}
	return
}

// Start starts the metadata manager.
func (m *metadataManager) Start() (err error) {
	if atomic.CompareAndSwapUint32(&m.state, common.StateStandby, common.StateStart) {
		defer func() {
			var newState uint32
			if err != nil {
				newState = common.StateStandby
			} else {
				newState = common.StateRunning
			}
			atomic.StoreUint32(&m.state, newState)
		}()
		err = m.onStart()
	}
	return
}

// Stop stops the metadata manager.
func (m *metadataManager) Stop() {
	if atomic.CompareAndSwapUint32(&m.state, common.StateRunning, common.StateShutdown) {
		defer atomic.StoreUint32(&m.state, common.StateStopped)
		m.onStop()
	}
}

// onStart creates the connection pool and loads the partitions.
func (m *metadataManager) onStart() (err error) {
	m.connPool = util.NewConnectPool()
	err = m.loadPartitions()
	return
}

// onStop stops each meta partitions.
func (m *metadataManager) onStop() {
	if m.partitions != nil {
		for _, partition := range m.partitions {
			partition.Stop()
		}
	}
	return
}

// LoadMetaPartition returns the meta partition with the specified volName.
func (m *metadataManager) getPartition(id uint64) (mp *MetaPartition, err error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mp, ok := m.partitions[id]
	if ok {
		return
	}
	err = errors.New(fmt.Sprintf("unknown meta partition: %d", id))
	return
}

func (m *metadataManager) loadPartitions() (err error) {
	var metaNodeInfo *proto.MetaNodeInfo
	for i := 0; i < 3; i++ {
		if metaNodeInfo, err = masterClient.NodeAPI().GetMetaNode(fmt.Sprintf("%s:%s", m.metaNode.localAddr,
			m.metaNode.listen)); err != nil {
			log.LogErrorf("loadPartitions: get MetaNode info fail: err(%v)", err)
			continue
		}
		break
	}

	if len(metaNodeInfo.PersistenceMetaPartitions) == 0 {
		log.LogWarnf("loadPartitions: length of PersistenceMetaPartitions is 0, ExpiredPartition check without effect")
	}

	// Check metadataDir directory
	fileInfo, err := os.Stat(m.rootDir)
	if err != nil {
		if os.IsNotExist(err) {
			err = os.MkdirAll(m.rootDir, 0755)
		} else {
			return err
		}
	}
	if !fileInfo.IsDir() {
		err = errors.New("metadataDir must be directory")
		return
	}
	// scan the data directory
	fileInfoList, err := ioutil.ReadDir(m.rootDir)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	for _, fileInfo := range fileInfoList {
		if fileInfo.IsDir() && strings.HasPrefix(fileInfo.Name(), partitionPrefix) {

			if isExpiredPartition(fileInfo.Name(), metaNodeInfo.PersistenceMetaPartitions) {
				log.LogErrorf("loadPartitions: find expired partition[%s], rename it and you can delete him manually",
					fileInfo.Name())
				oldName := path.Join(m.rootDir, fileInfo.Name())
				newName := path.Join(m.rootDir, ExpiredPartitionPrefix+fileInfo.Name()+"_"+strconv.FormatInt(time.Now().Unix(), 10))
				if tempErr := os.Rename(oldName, newName); tempErr != nil {
					log.LogErrorf("rename file has err:[%s]", tempErr.Error())
				}

				if len(fileInfo.Name()) > 10 && strings.HasPrefix(fileInfo.Name(), partitionPrefix) {
					log.LogErrorf("loadPartitions: find expired partition[%s], rename raft file",
						fileInfo.Name())
					partitionId := fileInfo.Name()[len(partitionPrefix):]
					oldRaftName := path.Join(m.metaNode.raftDir, partitionId)
					newRaftName := path.Join(m.metaNode.raftDir, ExpiredPartitionPrefix+partitionId+"_"+strconv.FormatInt(time.Now().Unix(), 10))
					log.LogErrorf("loadPartitions: find expired try rename raft file [%s] -> [%s]", oldRaftName, newRaftName)
					if _, tempErr := os.Stat(oldRaftName); tempErr != nil {
						log.LogWarnf("stat file [%s] has err:[%s]", oldRaftName, tempErr.Error())
					} else {
						if tempErr := os.Rename(oldRaftName, newRaftName); tempErr != nil {
							log.LogErrorf("rename file has err:[%s]", tempErr.Error())
						}
					}
				}

				continue
			}

			wg.Add(1)
			go func(fileName string) {
				var errload error
				start := time.Now()
				defer func() {
					if r := recover(); r != nil {
						log.LogErrorf("loadPartitions partition: %s, "+
							"error: %s, failed: %v", fileName, errload, r)
						log.LogFlush()
						panic(r)
					}
					if errload != nil {
						log.LogErrorf("loadPartitions partition: %s, "+
							"error: %s", fileName, errload)
						log.LogFlush()
						panic(errload)
					} else {
						log.LogInfof("load partition:[%s] end use time:[%s]", fileName, time.Now().Sub(start))
					}
				}()
				defer wg.Done()

				if len(fileName) < 10 {
					log.LogWarnf("ignore unknown partition dir: %s", fileName)
					return
				}
				var id uint64
				partitionId := fileName[len(partitionPrefix):]
				id, errload = strconv.ParseUint(partitionId, 10, 64)
				if errload != nil {
					log.LogWarnf("ignore path: %s,not partition", partitionId)
					return
				}

				partitionConfig := &MetaPartitionConfig{
					PartitionId: id,
					NodeId:      m.nodeId,
					RaftStore:   m.raftStore,
					RootDir:     path.Join(m.rootDir, fileName),
					ConnPool:    m.connPool,
				}

				if err = loadMetadata(partitionConfig); err != nil {
					log.LogWarnf("load meta data has err:%s ,ignore path: %s,not partition", err.Error(), partitionId)
					return
				}

				//if sotreType is rocksdb , so find rocksdir in path
				if partitionConfig.StoreType == proto.MetaTypeRocks {
					for _, dir := range m.rocksDirs {
						rocksdbDir := partitionPrefixPath(dir, partitionId)
						if _, err = os.Stat(rocksdbDir); err != nil {
							if os.IsNotExist(err) {
								err = nil
							} else {
								errload = err
								return
							}
						} else {
							partitionConfig.RocksDir = rocksdbDir
							break
						}
					}

					if partitionConfig.RocksDir == "" {
						dir, err := util.SelectDisk(m.rocksDirs)
						if err != nil {
							errload = err
							return
						}
						partitionConfig.RocksDir = partitionPrefixPath(dir, partitionId)
						if errload = os.MkdirAll(partitionConfig.RocksDir, os.ModePerm); errload != nil {
							return
						}
					}
				}

				partitionConfig.AfterStop = func() {
					m.detachPartition(id)
				}
				// check snapshot dir or backup
				snapshotDir := path.Join(partitionConfig.RootDir, snapshotDir)
				if _, errload = os.Stat(snapshotDir); errload != nil {
					backupDir := path.Join(partitionConfig.RootDir, snapshotBackup)
					if _, errload = os.Stat(backupDir); errload == nil {
						if errload = os.Rename(backupDir, snapshotDir); errload != nil {
							errload = errors.Trace(errload,
								fmt.Sprintf(": fail recover backup snapshot %s",
									snapshotDir))
							return
						}
					}
					errload = nil
				}
				partition, err := NewMetaPartition(partitionConfig, m)
				if err != nil {
					errload = errors.Trace(errload, fmt.Sprintf(": fail init meta partition:[%s]", err.Error()))
					return
				}
				errload = m.attachPartition(id, partition)
				if errload != nil {
					log.LogErrorf("load partition id=%d failed: %s.", id, errload.Error())
				}
			}(fileInfo.Name())
		}
	}
	wg.Wait()
	return
}

func (m *metadataManager) attachPartition(id uint64, partition *MetaPartition) (err error) {
	fmt.Println(fmt.Sprintf("start load metaPartition %v", id))
	partition.ForceSetMetaPartitionToLoadding()
	if err = partition.Start(); err != nil {
		log.LogErrorf("load meta partition %v fail: %v", id, err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.partitions[id] = partition
	log.LogInfof("load meta partition %v success", id)
	return
}

func (m *metadataManager) detachPartition(id uint64) (err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mp, has := m.partitions[id]; has {
		log.LogIfNotNil(mp.Reset())
		delete(m.partitions, id)
	} else {
		err = fmt.Errorf("unknown partition: %d", id)
	}
	return
}

func (m *metadataManager) createPartition(request *proto.CreateMetaPartitionRequest) (err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	partitionId := fmt.Sprintf("%d", request.PartitionID)

	mpc := &MetaPartitionConfig{
		PartitionId: request.PartitionID,
		VolName:     request.VolName,
		Start:       request.Start,
		End:         request.End,
		Cursor:      request.Start,
		Peers:       request.Members,
		Learners:    request.Learners,
		RaftStore:   m.raftStore,
		NodeId:      m.nodeId,
		RootDir:     path.Join(m.rootDir, partitionPrefix+partitionId),
		StoreType:   request.StoreType,
		ConnPool:    m.connPool,
	}

	// only allow to create MetaTypeMemory/ MetaTypeRocks mp
	if mpc.StoreType == proto.MetaTypeRocks {
		rocksPath, err := util.SelectDisk(m.rocksDirs)
		if err != nil {
			return err
		}
		mpc.RocksDir = partitionPrefixPath(rocksPath, partitionId)
	} else {
		log.LogDebugf("====xxx===createMetaPartition: %v\n", mpc)
		//mpc.StoreType = proto.MetaTypeMemory
	}

	mpc.AfterStop = func() {
		m.detachPartition(request.PartitionID)
	}

	if oldMp, ok := m.partitions[request.PartitionID]; ok {
		err = oldMp.IsEquareCreateMetaPartitionRequst(request)
		return
	}

	partition, err := NewMetaPartition(mpc, m)
	if err != nil {
		err = errors.NewErrorf("[createPartition]->%s", err.Error())
		return
	}
	if err = partition.PersistMetadata(); err != nil {
		err = errors.NewErrorf("[createPartition]->%s", err.Error())
		return
	}

	if err = partition.Start(); err != nil {
		os.RemoveAll(mpc.RootDir)
		log.LogErrorf("load meta partition %v fail: %v", request.PartitionID, err)
		err = errors.NewErrorf("[createPartition]->%s", err.Error())
		return
	}

	m.partitions[request.PartitionID] = partition
	log.LogInfof("load meta partition %v success", request.PartitionID)

	return
}

func (m *metadataManager) deletePartition(id uint64) (err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mp, has := m.partitions[id]
	if !has {
		return
	}
	mp.Reset()
	delete(m.partitions, id)
	return
}

func (m *metadataManager) expiredPartition(id uint64) (err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mp, has := m.partitions[id]
	if !has {
		return
	}
	mp.Expired()
	delete(m.partitions, id)
	return
}

// Range scans all the meta partitions.
func (m *metadataManager) Range(f func(i uint64, p *MetaPartition) bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for k, v := range m.partitions {
		if !f(k, v) {
			return
		}
	}
}

// GetPartition returns the meta partition with the given ID.
func (m *metadataManager) GetPartition(id uint64) (mp *MetaPartition, err error) {
	mp, err = m.getPartition(id)
	return
}

// MarshalJSON only marshals the base information of every partition.
func (m *metadataManager) MarshalJSON() (data []byte, err error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return json.Marshal(m.partitions)
}

// NewMetadataManager returns a new metadata manager.
func NewMetadataManager(metaNode *MetaNode) *metadataManager {
	return &metadataManager{
		nodeId:     metaNode.nodeId,
		zoneName:   metaNode.zoneName,
		rootDir:    metaNode.metadataDir,
		rocksDirs:  metaNode.rocksDirs,
		raftStore:  metaNode.raftStore,
		partitions: make(map[uint64]*MetaPartition),
		metaNode:   metaNode,
	}
}

// isExpiredPartition return whether one partition is expired
// if one partition does not exist in master, we decided that it is one expired partition
func isExpiredPartition(fileName string, partitions []uint64) (expiredPartition bool) {
	if len(partitions) == 0 {
		return true
	}

	partitionId := fileName[len(partitionPrefix):]
	id, err := strconv.ParseUint(partitionId, 10, 64)
	if err != nil {
		log.LogWarnf("isExpiredPartition: %s, check error [%v], skip this check", partitionId, err)
		return true
	}

	for _, existId := range partitions {
		if existId == id {
			return false
		}
	}
	return true
}
