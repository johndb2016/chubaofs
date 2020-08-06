// Copyright 2018 The CFS Authors.
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

package master

import (
	"encoding/json"
	"fmt"
	"github.com/chubaofs/chubaofs/proto"
	"github.com/chubaofs/chubaofs/third_party/juju/errors"
	"github.com/chubaofs/chubaofs/util"
	"github.com/chubaofs/chubaofs/util/log"
	"sync"
	"time"
)

type Vol struct {
	Name                       string
	Owner                      string
	VolType                    string
	dpReplicaNum               uint8
	mpReplicaNum               uint8
	MinWritableDPNum           uint64
	threshold                  float32
	Status                     uint8
	Capacity                   uint64 //GB
	AvailSpaceAllocated        uint64 //GB
	UsedSpace                  uint64 //GB
	MetaPartitions             map[uint64]*MetaPartition
	mpsLock                    sync.RWMutex
	dataPartitions             *DataPartitionMap
	enableToken                bool
	tokens                     map[string]*proto.Token
	tokensLock                 sync.RWMutex
	mpsCache                   []byte
	viewCache                  []byte
	isAllMetaPartitionReadonly bool
	createMpLock               sync.Mutex
	createDpLock               sync.Mutex
	sync.RWMutex
}

func NewVol(name, owner, volType string, replicaNum uint8, capacity, minWritableDPNum uint64, enableToken bool) (vol *Vol) {
	vol = &Vol{Name: name, VolType: volType, MetaPartitions: make(map[uint64]*MetaPartition, 0)}
	vol.Owner = owner
	vol.dataPartitions = NewDataPartitionMap(name)
	vol.dpReplicaNum = replicaNum
	vol.threshold = DefaultMetaPartitionThreshold
	if replicaNum%2 == 0 {
		vol.mpReplicaNum = replicaNum + 1
	} else {
		vol.mpReplicaNum = replicaNum
	}
	vol.Capacity = capacity
	vol.MinWritableDPNum = minWritableDPNum
	vol.viewCache = make([]byte, 0)
	vol.enableToken = enableToken
	vol.tokens = make(map[string]*proto.Token, 0)
	return
}

func (vol *Vol) String() string {
	return fmt.Sprintf("name[%v],dpNum[%v],mpNum[%v],cap[%v],status[%v]",
		vol.Name, vol.dpReplicaNum, vol.mpReplicaNum, vol.Capacity, vol.Status)
}

func (vol *Vol) getToken(token string) (tokenObj *proto.Token, err error) {
	vol.tokensLock.Lock()
	defer vol.tokensLock.Unlock()
	tokenObj, ok := vol.tokens[token]
	if !ok {
		return nil, TokenNotFound
	}
	return
}

func (vol *Vol) deleteToken(token string) {
	vol.tokensLock.RLock()
	defer vol.tokensLock.RUnlock()
	delete(vol.tokens, token)
}

func (vol *Vol) putToken(token *proto.Token) {
	vol.tokensLock.Lock()
	defer vol.tokensLock.Unlock()
	vol.tokens[token.Value] = token
	return
}

func (vol *Vol) AddMetaPartition(mp *MetaPartition) {
	vol.mpsLock.Lock()
	defer vol.mpsLock.Unlock()
	if _, ok := vol.MetaPartitions[mp.PartitionID]; !ok {
		vol.MetaPartitions[mp.PartitionID] = mp
	}
}

func (vol *Vol) getMetaPartition(partitionID uint64) (mp *MetaPartition, err error) {
	vol.mpsLock.RLock()
	defer vol.mpsLock.RUnlock()
	mp, ok := vol.MetaPartitions[partitionID]
	if !ok {
		err = MetaPartitionNotFound
	}
	return
}

func (vol *Vol) getMaxPartitionID() (maxPartitionID uint64) {
	vol.mpsLock.RLock()
	defer vol.mpsLock.RUnlock()
	for id := range vol.MetaPartitions {
		if id > maxPartitionID {
			maxPartitionID = id
		}
	}
	return
}

func (vol *Vol) getDataPartitionsView(liveRate float32) (body []byte, err error) {
	if liveRate < NodesAliveRate {
		body = make([]byte, 0)
		return
	}
	return vol.dataPartitions.updateDataPartitionResponseCache(false, 0)
}

func (vol *Vol) getDataPartitionByID(partitionID uint64) (dp *DataPartition, err error) {
	return vol.dataPartitions.getDataPartition(partitionID)
}

func (vol *Vol) initDataPartitions(c *Cluster) {
	//init ten data partitions
	for i := 0; i < DefaultInitDataPartitions; i++ {
		c.createDataPartition(vol.Name, vol.VolType)
	}
	return
}

func (vol *Vol) checkDataPartitions(c *Cluster) (readWriteDataPartitions int) {
	vol.dataPartitions.RLock()
	defer vol.dataPartitions.RUnlock()
	for _, dp := range vol.dataPartitions.dataPartitionMap {
		dp.checkReplicaStatus(c.cfg.DataPartitionTimeOutSec)
		dp.checkStatus(c.Name, true, c.cfg.DataPartitionTimeOutSec)
		dp.checkMiss(c.Name, c.leaderInfo.addr, c.cfg.DataPartitionMissSec, c.cfg.DataPartitionWarnInterval)
		dp.checkReplicaNum(c, vol.Name)
		if dp.Status == proto.ReadWrite {
			readWriteDataPartitions++
		}
		dp.checkDiskError(c.Name, c.leaderInfo.addr)
		tasks := dp.checkReplicationTask(c.Name)
		if len(tasks) != 0 {
			c.putDataNodeTasks(tasks)
		}
	}
	return
}

func (vol *Vol) LoadDataPartition(c *Cluster) {
	needCheckDataPartitions, startIndex := vol.dataPartitions.getNeedCheckDataPartitions(c.cfg.LoadDataPartitionFrequencyTime)
	if len(needCheckDataPartitions) == 0 {
		return
	}
	c.waitLoadDataPartitionResponse(needCheckDataPartitions)
	msg := fmt.Sprintf("action[LoadDataPartition] vol[%v],checkStartIndex:%v checkCount:%v",
		vol.Name, startIndex, len(needCheckDataPartitions))
	log.LogInfo(msg)
}

func (vol *Vol) ReleaseDataPartitions(releaseCount int, afterLoadSeconds int64) {
	needReleaseDataPartitions, startIndex := vol.dataPartitions.getNeedReleaseDataPartitions(releaseCount, afterLoadSeconds)
	if len(needReleaseDataPartitions) == 0 {
		return
	}
	vol.dataPartitions.releaseDataPartitions(needReleaseDataPartitions)
	msg := fmt.Sprintf("action[ReleaseDataPartitions] vol[%v] release data partition start:%v releaseCount:%v",
		vol.Name, startIndex, len(needReleaseDataPartitions))
	log.LogInfo(msg)
}

func (vol *Vol) checkMetaPartitions(c *Cluster) {
	var tasks []*proto.AdminTask
	maxPartitionID := vol.getMaxPartitionID()
	mps := vol.cloneMetaPartitionMap()
	var (
		doSplit bool
		err     error
	)
	for _, mp := range mps {
		doSplit = mp.checkStatus(true, int(vol.mpReplicaNum), maxPartitionID)
		if doSplit {
			nextStart := mp.Start + mp.MaxInodeID + defaultMetaPartitionInodeIDStep
			if err = vol.splitMetaPartition(c, mp, nextStart); err != nil {
				Warn(c.Name, fmt.Sprintf("vol[%v],meta partition[%v] splits failed,err[%v]", vol.Name, mp.PartitionID, err))
			}
		}
		mp.checkReplicaLeader()
		mp.checkReplicaNum(c, vol.Name, vol.mpReplicaNum)
		mp.checkEnd(c, maxPartitionID)
		mp.checkReplicaMiss(c.Name, c.leaderInfo.addr, DefaultMetaPartitionTimeOutSec, DefaultMetaPartitionWarnInterval)
		tasks = append(tasks, mp.GenerateReplicaTask(c.Name, vol.Name)...)
	}
	c.putMetaNodeTasks(tasks)
}

func (vol *Vol) cloneMetaPartitionMap() (mps map[uint64]*MetaPartition) {
	mps = make(map[uint64]*MetaPartition, 0)
	vol.mpsLock.RLock()
	defer vol.mpsLock.RUnlock()
	for _, mp := range vol.MetaPartitions {
		mps[mp.PartitionID] = mp
	}
	return
}

func (vol *Vol) statSpace() (used, total uint64) {
	vol.dataPartitions.RLock()
	defer vol.dataPartitions.RUnlock()
	for _, dp := range vol.dataPartitions.dataPartitions {
		total = total + dp.total
		used = used + dp.getMaxUsedSize()
	}
	return
}

func (vol *Vol) setStatus(status uint8) {
	vol.Lock()
	defer vol.Unlock()
	vol.Status = status
}

func (vol *Vol) getStatus() uint8 {
	vol.RLock()
	defer vol.RUnlock()
	return vol.Status
}

func (vol *Vol) setCapacity(capacity uint64) {
	vol.Lock()
	defer vol.Unlock()
	vol.Capacity = capacity
}

func (vol *Vol) getCapacity() uint64 {
	vol.RLock()
	defer vol.RUnlock()
	return vol.Capacity
}
func (vol *Vol) setMinWritableDPNum(minWritableDPNum uint64) {
	vol.Lock()
	defer vol.Unlock()
	vol.MinWritableDPNum = minWritableDPNum
}

func (vol *Vol) getMinWritableDPNum() uint64 {
	vol.RLock()
	defer vol.RUnlock()
	return vol.MinWritableDPNum
}

func (vol *Vol) checkNeedAutoCreateDataPartitions(c *Cluster) {
	if vol.getStatus() == VolMarkDelete {
		return
	}
	if vol.getCapacity() == 0 {
		return
	}
	if vol.UsedSpace >= vol.getCapacity() {
		vol.setAllDataPartitionsToReadOnly()
		return
	} else {
		vol.setStatus(VolNormal)
	}
	if vol.getStatus() == VolNormal && !c.DisableAutoAlloc {
		vol.autoCreateDataPartitions(c)
	}
}

func (vol *Vol) autoCreateDataPartitions(c *Cluster) {
	if vol.Capacity == 0 {
		vol.Capacity = vol.getTotalSpace() / util.GB
	}
	usedRatio := float64(vol.UsedSpace) / float64(vol.Capacity)
	availRatio := float64(vol.AvailSpaceAllocated) / float64(vol.Capacity)
	lackCount := int(vol.MinWritableDPNum) - vol.dataPartitions.readWriteDataPartitions
	if (vol.Capacity > 200000 && vol.dataPartitions.readWriteDataPartitions < 200) || vol.dataPartitions.readWriteDataPartitions < MinReadWriteDataPartitions || (usedRatio > VolWarningRatio && vol.isTooSmallAvailSpace(availRatio)) ||
		(vol.MinWritableDPNum != 0 && lackCount > 0) {
		count := vol.calculateExpandNum()
		if lackCount > count {
			count = lackCount
		}
		log.LogInfof("action[autoCreateDataPartitions] vol[%v] count[%v],usedRatio[%v],availRatio[%v]", vol.Name, count, usedRatio, availRatio)
		for i := 0; i < count; i++ {
			if c.DisableAutoAlloc {
				return
			}
			c.createDataPartition(vol.Name, vol.VolType)
		}
	}
}

func (vol *Vol) isTooSmallAvailSpace(availRatio float64) bool {
	return availRatio < VolMinAvailSpaceRatio && vol.AvailSpaceAllocated < util.TB && vol.AvailSpaceAllocated > 0
}

func (vol *Vol) calculateExpandNum() (count int) {
	calCount := float64(vol.Capacity) * float64(VolExpandDataPartitionStepRatio) * float64(util.GB) / float64(util.DefaultDataPartitionSize)
	switch {
	case calCount < MinReadWriteDataPartitions:
		count = MinReadWriteDataPartitions
	case calCount > VolMaxExpandDataPartitionCount:
		count = VolMaxExpandDataPartitionCount
	default:
		count = int(calCount)
	}
	return
}

func (vol *Vol) setAllDataPartitionsToReadOnly() {
	vol.dataPartitions.setAllDataPartitionsToReadOnly()
}

func (vol *Vol) getTotalUsedSpace() uint64 {
	return vol.dataPartitions.getTotalUsedSpace()
}

func (vol *Vol) getTotalSpace() uint64 {
	return vol.dataPartitions.getTotalSpace()
}

func (vol *Vol) updateViewCache(c *Cluster) {
	liveMetaNodesRate := c.getLiveMetaNodesRate()
	liveDataNodesRate := c.getLiveDataNodesRate()
	if liveMetaNodesRate < NodesAliveRate || liveDataNodesRate < NodesAliveRate {
		return
	}
	view := NewVolView(vol.Name, vol.VolType, vol.Status)
	mpViews := vol.getMetaPartitionsView(c.reloadMetadataTime)
	view.MetaPartitions = mpViews
	mpsBody, err := json.Marshal(mpViews)
	if err != nil {
		log.LogErrorf("action[updateViewCache] failed,vol[%v],err[%v]", vol.Name, err)
		return
	}
	vol.setMpsCache(mpsBody)
	dpResps := vol.dataPartitions.GetDataPartitionsView(0)
	view.DataPartitions = dpResps
	body, err := json.Marshal(view)
	if err != nil {
		log.LogErrorf("action[updateViewCache] failed,vol[%v],err[%v]", vol.Name, err)
		return
	}
	vol.setViewCache(body)
}

func (vol *Vol) getMetaPartitionsView(reloadMetadataTime int64) (mpViews []*MetaPartitionView) {
	vol.mpsLock.RLock()
	defer vol.mpsLock.RUnlock()
	mpViews = make([]*MetaPartitionView, 0)
	readonlyCount := 0
	for _, mp := range vol.MetaPartitions {
		if mp.Status != proto.ReadWrite {
			readonlyCount++
		}
		mpViews = append(mpViews, getMetaPartitionView(mp))
	}
	if time.Now().Unix()-reloadMetadataTime < 120 && readonlyCount == len(vol.MetaPartitions) {
		mpViews = make([]*MetaPartitionView, 0)
		vol.isAllMetaPartitionReadonly = true
	} else {
		vol.isAllMetaPartitionReadonly = false
	}
	return
}

func (vol *Vol) setMpsCache(body []byte) {
	vol.Lock()
	defer vol.Unlock()
	vol.mpsCache = body
}

func (vol *Vol) getMpsCache() []byte {
	vol.RLock()
	defer vol.RUnlock()
	return vol.mpsCache
}

func (vol *Vol) setViewCache(body []byte) {
	vol.Lock()
	defer vol.Unlock()
	vol.viewCache = body
}

func (vol *Vol) getViewCache() []byte {
	vol.RLock()
	defer vol.RUnlock()
	return vol.viewCache
}

func (vol *Vol) checkStatus(c *Cluster) {
	defer func() {
		if r := recover(); r != nil {
			log.LogWarnf("vol checkStatus occurred panic,err[%v]", r)
			WarnBySpecialUmpKey(fmt.Sprintf("%v_%v_scheduling_job_panic", c.Name, UmpModuleName),
				"vol checkStatus occurred panic")
		}
	}()
	vol.updateViewCache(c)
	vol.Lock()
	defer vol.Unlock()

	if vol.Status == VolMarkDelete {
		metaTasks := vol.getDeleteMetaTasks()
		dataTasks := vol.getDeleteDataTasks()
		if len(metaTasks) == 0 && len(dataTasks) == 0 {
			vol.deleteVolFromStore(c)
		}
		c.putMetaNodeTasks(metaTasks)
		c.putDataNodeTasks(dataTasks)
		return
	}

}

func (vol *Vol) deleteVolFromStore(c *Cluster) (err error) {

	if err = c.syncDeleteVol(vol); err != nil {
		return
	}
	//delete mp and dp metadata first, then delete vol in case new vol with same name create
	vol.deleteDataPartitionsFromStore(c)
	vol.deleteMetaPartitionsFromStore(c)
	vol.deleteTokensFromStore(c)
	c.deleteVol(vol.Name)
	c.volSpaceStat.Delete(vol.Name)
	return
}

func (vol *Vol) deleteTokensFromStore(c *Cluster) {
	vol.tokensLock.RLock()
	defer vol.tokensLock.RUnlock()
	for _, token := range vol.tokens {
		c.syncDeleteToken(token)
	}
}

func (vol *Vol) deleteMetaPartitionsFromStore(c *Cluster) {
	vol.mpsLock.RLock()
	defer vol.mpsLock.RUnlock()
	for _, mp := range vol.MetaPartitions {
		c.syncDeleteMetaPartition(vol.Name, mp)
	}
	return
}

func (vol *Vol) deleteDataPartitionsFromStore(c *Cluster) {
	vol.dataPartitions.RLock()
	defer vol.dataPartitions.RUnlock()
	for _, dp := range vol.dataPartitions.dataPartitions {
		c.syncDeleteDataPartition(vol.Name, dp)
	}

}

func (vol *Vol) deleteDataPartitionsFromCache(dp *DataPartition) {
	vol.dataPartitions.deleteDataPartition(dp)
}

func (vol *Vol) getDeleteMetaTasks() (tasks []*proto.AdminTask) {
	vol.mpsLock.RLock()
	defer vol.mpsLock.RUnlock()
	tasks = make([]*proto.AdminTask, 0)
	//if replica has removed,the length of tasks will be zero
	for _, mp := range vol.MetaPartitions {
		for _, replica := range mp.Replicas {
			tasks = append(tasks, replica.generateDeleteReplicaTask(mp.PartitionID))
		}
	}
	return
}

func (vol *Vol) getDeleteDataTasks() (tasks []*proto.AdminTask) {
	tasks = make([]*proto.AdminTask, 0)
	vol.dataPartitions.RLock()
	defer vol.dataPartitions.RUnlock()
	//if replica has removed,the length of tasks will be zero
	for _, dp := range vol.dataPartitions.dataPartitions {
		for _, replica := range dp.Replicas {
			tasks = append(tasks, dp.GenerateDeleteTask(replica.Addr))
		}
	}
	return
}

func (vol *Vol) doSplitMetaPartition(c *Cluster, mp *MetaPartition, end uint64) (nextMp *MetaPartition, err error) {
	mp.Lock()
	defer mp.Unlock()
	log.LogWarnf("action[splitMetaPartition],partition[%v],start[%v],end[%v]", mp.PartitionID, mp.Start, mp.End)
	if err = mp.updateEnd(c, end); err != nil {
		return
	}
	if nextMp, err = vol.doCreateMetaPartition(c, mp.End+1, defaultMaxMetaPartitionInodeID); err != nil {
		Warn(c.Name, fmt.Sprintf("action[updateEnd] clusterID[%v] partitionID[%v] create meta partition err[%v]",
			c.Name, mp.PartitionID, err))
		log.LogErrorf("action[updateEnd] partitionID[%v] err[%v]", mp.PartitionID, err)
		return
	}
	return
}

func (vol *Vol) splitMetaPartition(c *Cluster, mp *MetaPartition, end uint64) (err error) {
	if c.DisableAutoAlloc {
		return
	}
	vol.createMpLock.Lock()
	defer vol.createMpLock.Unlock()
	if end < mp.Start {
		err = fmt.Errorf("end[%v] less than mp.start[%v]", end, mp.Start)
		return
	}
	maxPartitionID := vol.getMaxPartitionID()
	if maxPartitionID != mp.PartitionID {
		err = fmt.Errorf("mp[%v] is not the last meta partition[%v]", mp.PartitionID, maxPartitionID)
		return
	}
	nextMp, err := vol.doSplitMetaPartition(c, mp, end)
	if err != nil {
		return
	}
	vol.AddMetaPartition(nextMp)
	log.LogWarnf("action[splitMetaPartition],next partition[%v],start[%v],end[%v]", nextMp.PartitionID, nextMp.Start, nextMp.End)
	return
}

func (vol *Vol) batchCreateMetaPartition(c *Cluster, count int) (err error) {
	// initialize k meta partitionMap at a time
	var (
		start uint64
		end   uint64
	)
	if count < defaultInitMetaPartitionCount {
		count = defaultInitMetaPartitionCount
	}
	if count > defaultMaxInitMetaPartitionCount {
		count = defaultMaxInitMetaPartitionCount
	}
	for index := 0; index < count; index++ {
		if index != 0 {
			start = end + 1
		}
		end = defaultMetaPartitionInodeIDStep * uint64(index+1)
		if index == count-1 {
			end = defaultMaxMetaPartitionInodeID
		}
		if err = vol.createMetaPartition(c, start, end); err != nil {
			log.LogErrorf("action[initMetaPartitions] vol[%v] init meta partition err[%v]", vol.Name, err)
			break
		}
	}
	if len(vol.MetaPartitions) != count {
		err = fmt.Errorf("action[initMetaPartitions] vol[%v] init meta partition failed,mpCount[%v],expectCount[%v]",
			vol.Name, len(vol.MetaPartitions), count)
	}
	return
}

func (vol *Vol) createMetaPartition(c *Cluster, start, end uint64) (err error) {
	vol.createMpLock.Lock()
	defer vol.createMpLock.Unlock()
	var mp *MetaPartition
	if mp, err = vol.doCreateMetaPartition(c, start, end); err != nil {
		return
	}
	vol.AddMetaPartition(mp)
	return
}

func (vol *Vol) doCreateMetaPartition(c *Cluster, start, end uint64) (mp *MetaPartition, err error) {
	var (
		hosts       []string
		partitionID uint64
		peers       []proto.Peer
		wg          sync.WaitGroup
	)
	errChannel := make(chan error, vol.mpReplicaNum)
	if hosts, peers, err = c.ChooseTargetMetaHosts(int(vol.mpReplicaNum)); err != nil {
		return nil, errors.Trace(err)
	}
	log.LogInfof("target meta hosts:%v,peers:%v", hosts, peers)
	if partitionID, err = c.idAlloc.allocateMetaPartitionID(); err != nil {
		return nil, errors.Trace(err)
	}
	mp = NewMetaPartition(partitionID, start, end, vol.mpReplicaNum, vol.Name)
	mp.setPersistenceHosts(hosts)
	mp.setPeers(peers)
	for _, host := range hosts {
		wg.Add(1)
		go func(host string) {
			defer func() {
				wg.Done()
			}()
			if err = c.syncCreateMetaPartitionToMetaNode(host, mp); err != nil {
				errChannel <- err
				return
			}
			mp.Lock()
			defer mp.Unlock()
			if err = mp.createPartitionSuccessTriggerOperator(host, c); err != nil {
				errChannel <- err
			}
		}(host)
	}
	wg.Wait()
	select {
	case err = <-errChannel:
		return nil, errors.Trace(err)
	default:
		mp.Status = proto.ReadWrite
	}
	if err = c.syncAddMetaPartition(vol.Name, mp); err != nil {
		return nil, errors.Trace(err)
	}
	log.LogInfof("action[CreateMetaPartition] success,volName[%v],partition[%v]", vol.Name, partitionID)
	return
}
