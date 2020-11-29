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

package master

import (
	"fmt"
	"github.com/chubaofs/chubaofs/util/log"
	"math"
	"strconv"
	"time"
)

func (c *Cluster) scheduleToLoadMetaPartitions() {
	go func() {
		for {
			if c.partition != nil && c.partition.IsRaftLeader() {
				if c.vols != nil {
					c.checkLoadMetaPartitions()
				}
			}
			time.Sleep(2 * time.Second * defaultIntervalToCheckDataPartition)
		}
	}()
}

func (c *Cluster) checkLoadMetaPartitions() {
	defer func() {
		if r := recover(); r != nil {
			log.LogWarnf("checkDiskRecoveryProgress occurred panic,err[%v]", r)
			WarnBySpecialKey(fmt.Sprintf("%v_%v_scheduling_job_panic", c.Name, ModuleName),
				"checkDiskRecoveryProgress occurred panic")
		}
	}()
	vols := c.allVols()
	for _, vol := range vols {
		mps := vol.cloneMetaPartitionMap()
		for _, mp := range mps {
			c.doLoadMetaPartition(mp)
		}
	}
}

func (mp *MetaPartition) checkSnapshot(clusterID string) {
	if len(mp.LoadResponse) == 0 {
		return
	}
	if !mp.doCompare() {
		return
	}
	if !mp.isSameApplyID() {
		return
	}
	mp.checkInodeCount(clusterID)
	mp.checkDentryCount(clusterID)
}

func (mp *MetaPartition) doCompare() bool {
	for _, lr := range mp.LoadResponse {
		if !lr.DoCompare {
			return false
		}
	}
	return true
}

func (mp *MetaPartition) isSameApplyID() bool {
	rst := true
	applyID := mp.LoadResponse[0].ApplyID
	for _, loadResponse := range mp.LoadResponse {
		if applyID != loadResponse.ApplyID {
			rst = false
		}
	}
	return rst
}

func (mp *MetaPartition) checkInodeCount(clusterID string) {
	isEqual := true
	maxInode := mp.LoadResponse[0].MaxInode
	for _, loadResponse := range mp.LoadResponse {
		diff := math.Abs(float64(loadResponse.MaxInode) - float64(maxInode))
		if diff > defaultRangeOfCountDifferencesAllowed {
			isEqual = false
		}
	}

	if !isEqual {
		msg := fmt.Sprintf("inode count is not equal,vol[%v],mpID[%v],", mp.VolName, mp.PartitionID)
		for _, lr := range mp.LoadResponse {
			inodeCountStr := strconv.FormatUint(lr.MaxInode, 10)
			applyIDStr := strconv.FormatUint(uint64(lr.ApplyID), 10)
			msg = msg + lr.Addr + " applyId[" + applyIDStr + "] maxInode[" + inodeCountStr + "],"
		}
		Warn(clusterID, msg)
	}
}

func (mp *MetaPartition) checkDentryCount(clusterID string) {
	isEqual := true
	dentryCount := mp.LoadResponse[0].DentryCount
	for _, loadResponse := range mp.LoadResponse {
		diff := math.Abs(float64(loadResponse.DentryCount) - float64(dentryCount))
		if diff > defaultRangeOfCountDifferencesAllowed {
			isEqual = false
		}
	}

	if !isEqual {
		msg := fmt.Sprintf("dentry count is not equal,vol[%v],mpID[%v],", mp.VolName, mp.PartitionID)
		for _, lr := range mp.LoadResponse {
			dentryCountStr := strconv.FormatUint(lr.DentryCount, 10)
			applyIDStr := strconv.FormatUint(uint64(lr.ApplyID), 10)
			msg = msg + lr.Addr + " applyId[" + applyIDStr + "] dentryCount[" + dentryCountStr + "],"
		}
		Warn(clusterID, msg)
	}
}

func (c *Cluster) scheduleToCheckMetaPartitionRecoveryProgress() {
	go func() {
		for {
			if c.partition != nil && c.partition.IsRaftLeader() {
				if c.vols != nil {
					c.checkMetaPartitionRecoveryProgress()
					c.checkMigratedMetaPartitionRecoveryProgress()
				}
			}
			time.Sleep(3 * time.Second * defaultIntervalToCheckDataPartition)
		}
	}()
}

func (c *Cluster) checkMetaPartitionRecoveryProgress() {
	defer func() {
		if r := recover(); r != nil {
			log.LogWarnf("checkMetaPartitionRecoveryProgress occurred panic,err[%v]", r)
			WarnBySpecialKey(fmt.Sprintf("%v_%v_scheduling_job_panic", c.Name, ModuleName),
				"checkMetaPartitionRecoveryProgress occurred panic")
		}
	}()

	var diff float64
	c.BadMetaPartitionIds.Range(func(key, value interface{}) bool {
		badMetaPartitionIds := value.([]uint64)
		newBadMpIds := make([]uint64, 0)
		for _, partitionID := range badMetaPartitionIds {
			partition, err := c.getMetaPartitionByID(partitionID)
			if err != nil {
				continue
			}
			vol, err := c.getVol(partition.VolName)
			if err != nil {
				continue
			}
			if len(partition.Replicas) == 0 || len(partition.Replicas) < int(vol.mpReplicaNum) {
				continue
			}
			diff = partition.getMinusOfMaxInodeID()
			if diff < defaultMinusOfMaxInodeID {
				partition.IsRecover = false
				partition.RLock()
				c.syncUpdateMetaPartition(partition)
				partition.RUnlock()
				Warn(c.Name, fmt.Sprintf("action[checkMetaPartitionRecoveryProgress] clusterID[%v],vol[%v] partitionID[%v] has recovered success", c.Name, partition.VolName, partitionID))
			} else {
				newBadMpIds = append(newBadMpIds, partitionID)
			}
		}

		if len(newBadMpIds) == 0 {
			Warn(c.Name, fmt.Sprintf("action[checkMetaPartitionRecoveryProgress] clusterID[%v],node[%v] has recovered success", c.Name, key))
			c.BadMetaPartitionIds.Delete(key)
		} else {
			c.BadMetaPartitionIds.Store(key, newBadMpIds)
		}

		return true
	})
}

func (c *Cluster) scheduleToCheckAutoMetaPartitionCreation() {
	go func() {
		// check volumes after switching leader two minutes
		time.Sleep(2 * time.Minute)
		for {
			if c.partition != nil && c.partition.IsRaftLeader() {
				vols := c.copyVols()
				for _, vol := range vols {
					vol.checkAutoMetaPartitionCreation(c)
				}
			}
			time.Sleep(2 * time.Second * defaultIntervalToCheckDataPartition)
		}
	}()
}

func (vol *Vol) checkAutoMetaPartitionCreation(c *Cluster) {
	defer func() {
		if r := recover(); r != nil {
			log.LogWarnf("checkAutoMetaPartitionCreation occurred panic,err[%v]", r)
			WarnBySpecialKey(fmt.Sprintf("%v_%v_scheduling_job_panic", c.Name, ModuleName),
				"checkAutoMetaPartitionCreation occurred panic")
		}
	}()
	if vol.status() == markDelete {
		return
	}
	if vol.status() == normal && !c.DisableAutoAllocate {
		vol.autoCreateMetaPartitions(c)
	}
}

func (vol *Vol) autoCreateMetaPartitions(c *Cluster) {
	count := vol.checkAndUpdateWritableMpCount()
	if count < int(vol.MinWritableMPNum) {
		maxPartitionID := vol.maxPartitionID()
		mp, err := vol.metaPartition(maxPartitionID)
		if err != nil {
			log.LogErrorf("action[autoCreateMetaPartitions],cluster[%v],vol[%v],err[%v]", c.Name, vol.Name, err)
			return
		}
		// wait for leader ready
		_, err = mp.getMetaReplicaLeader()
		if err != nil {
			log.LogWarnf("action[autoCreateMetaPartitions],cluster[%v],vol[%v],err[%v],create it later", c.Name, vol.Name, err)
			// wait for leader ready
			time.Sleep(2 * time.Second * defaultIntervalToCheckDataPartition)
			return
		}
		var nextStart uint64
		if mp.MaxInodeID <= 0 {
			nextStart = mp.Start + defaultMetaPartitionInodeIDStep
		} else {
			nextStart = mp.MaxInodeID + defaultMetaPartitionInodeIDStep
		}
		if err = vol.splitMetaPartition(c, mp, nextStart); err != nil {
			msg := fmt.Sprintf("cluster[%v],vol[%v],meta partition[%v] splits failed,err[%v]",
				c.Name, vol.Name, mp.PartitionID, err)
			Warn(c.Name, msg)
		}
	}
}